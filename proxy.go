package main

import (
	"context"
	"io"
	"log"
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

// Proxy is a plain TCP forwarder: it accepts clients on LISTEN_HOST:LISTEN_PORT
// and opens a kernel TCP connection to CONNECT_IP:CONNECT_PORT. Before relaying
// any client bytes it asks the Injector to whiten the upstream flow.
type Proxy struct {
	cfg *Config
	inj *Injector
}

func NewProxy(cfg *Config, inj *Injector) *Proxy {
	return &Proxy{cfg: cfg, inj: inj}
}

func (p *Proxy) Run(ctx context.Context) error {
	addr := &net.TCPAddr{IP: net.ParseIP(p.cfg.ListenHost), Port: p.cfg.ListenPort}
	ln, err := net.ListenTCP("tcp4", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("listening on %s -> %s:%d (white SNI: %s)",
		ln.Addr(), p.cfg.ConnectIP, p.cfg.ConnectPort, p.cfg.WhiteSNI)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		c, err := ln.AcceptTCP()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go p.handle(ctx, c)
	}
}

func (p *Proxy) handle(ctx context.Context, in *net.TCPConn) {
	defer in.Close()

	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(p.cfg.InterfaceIP)}}
	target := net.JoinHostPort(p.cfg.ConnectIP, strconv.Itoa(p.cfg.ConnectPort))
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := dialer.DialContext(dialCtx, "tcp4", target)
	if err != nil {
		log.Printf("dial %s: %v", target, err)
		return
	}
	defer out.Close()

	srcPort := uint16(out.LocalAddr().(*net.TCPAddr).Port)
	info, werr := p.inj.WaitSynAck(srcPort)

	// Read the client's first TLS record (its real ClientHello) in full, size a
	// matching decoy, inject it ahead of the real one, then forward the record.
	first, rerr := readFirstRecord(in, time.Duration(p.cfg.HandshakeTimeoutMs)*time.Millisecond)
	if rerr != nil || len(first) == 0 {
		return
	}
	if werr == nil {
		decoy := PadClientHello(p.inj.decoy, len(first))
		if err := p.inj.InjectDecoy(srcPort, info, decoy); err != nil {
			log.Printf("inject %s (srcPort %d): %v", target, srcPort, err)
		}
	} else {
		log.Printf("whiten %s (srcPort %d): %v", target, srcPort, werr)
	}
	if _, err := out.Write(first); err != nil {
		return
	}

	var up atomic.Uint64
	up.Store(uint64(len(first)))
	relayDone := make(chan struct{})
	if werr == nil && p.cfg.DecoyRefreshKB > 0 {
		go p.refresh(srcPort, info, &up, relayDone)
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(&countWriter{w: out, n: &up}, in); done <- struct{}{} }()
	go func() { _, _ = io.Copy(in, out); done <- struct{}{} }()
	<-done
	close(relayDone)
}

// refresh re-injects the decoy at the current upstream send position every
// DECOY_REFRESH_KB to renew whitening on long uploads.
func (p *Proxy) refresh(srcPort uint16, info synInfo, up *atomic.Uint64, done <-chan struct{}) {
	ttl := p.inj.ttlFor(info)
	step := uint64(p.cfg.DecoyRefreshKB) * 1024
	next := step
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			n := up.Load()
			if n < next {
				continue
			}
			next = n + step
			_ = p.inj.inject(srcPort, info.ourSeqP1+uint32(n), info.srvSeq+1, ttl)
		}
	}
}

// readFirstRecord reads the client's first TLS record (the ClientHello) in full,
// coalescing across TCP segments so the decoy can be length-matched exactly
// instead of relying on the record arriving in a single read. It falls back to
// the 5-byte header for non-TLS first bytes. The deadline bounds the wait and is
// cleared before returning.
func readFirstRecord(c net.Conn, timeout time.Duration) ([]byte, error) {
	_ = c.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()

	hdr := make([]byte, 5)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return nil, err
	}
	if hdr[0] != 0x16 { // not a TLS handshake record
		return hdr, nil
	}
	recLen := int(hdr[3])<<8 | int(hdr[4])
	if recLen <= 0 || recLen > 16384 {
		return hdr, nil
	}
	out := make([]byte, 5+recLen)
	copy(out, hdr)
	if _, err := io.ReadFull(c, out[5:]); err != nil {
		return nil, err
	}
	return out, nil
}

type countWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.n.Add(uint64(n))
	}
	return n, err
}
