package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// Proxy is a plain TCP forwarder. For each outbound connection it coordinates
// with the Injector to run the configured bypass before relaying real bytes.
type Proxy struct {
	cfg      *Config
	inj      *Injector
	pool     *SNIPool
	mode     BypassMode
	stats    *Stats
	nextID   atomic.Uint64
}

func NewProxy(cfg *Config, inj *Injector, pool *SNIPool, mode BypassMode, st *Stats) *Proxy {
	return &Proxy{cfg: cfg, inj: inj, pool: pool, mode: mode, stats: st}
}

func (p *Proxy) Run(ctx context.Context) error {
	addr := &net.TCPAddr{IP: net.ParseIP(p.cfg.ListenHost), Port: p.cfg.ListenPort}
	ln, err := net.ListenTCP("tcp4", addr)
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Printf("listening on %s -> %s:%d", ln.Addr(), p.cfg.ConnectIP, p.cfg.ConnectPort)

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
	connID := p.nextID.Add(1)
	p.stats.Active.Add(1)
	defer p.stats.Active.Add(-1)
	defer in.Close()
	defer p.pool.Release(connID)

	ifaceIP := net.ParseIP(p.cfg.InterfaceIP).To4()
	connectIP := net.ParseIP(p.cfg.ConnectIP).To4()
	if ifaceIP == nil || connectIP == nil {
		log.Printf("invalid ipv4 in config")
		p.stats.Failed.Add(1)
		return
	}

	sni := p.pool.Next(connID)
	fakeData := BuildClientHello(sni)

	var cs *connState
	dialer := net.Dialer{
		LocalAddr: &net.TCPAddr{IP: ifaceIP},
		Control: func(network, address string, rc syscall.RawConn) error {
			return rc.Control(func(fd uintptr) {
				sa, err := syscall.Getsockname(int(fd))
				if err != nil {
					return
				}
				sa4, ok := sa.(*syscall.SockaddrInet4)
				if !ok {
					return
				}
				cs = &connState{
					done:        make(chan struct{}),
					fakePayload: fakeData,
					srcPort:     uint16(sa4.Port),
					dstPort:     uint16(p.cfg.ConnectPort),
					mode:        p.mode,
					lowTTL:      uint8(p.cfg.LowTTLValue),
				}
				copy(cs.srcIP[:], ifaceIP)
				copy(cs.dstIP[:], connectIP)
				p.inj.register(cs)
			})
		},
	}

	target := net.JoinHostPort(p.cfg.ConnectIP, strconv.Itoa(p.cfg.ConnectPort))
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := dialer.DialContext(dialCtx, "tcp4", target)
	if err != nil {
		if cs != nil {
			cs.finish(err)
			p.inj.remove(cs)
		}
		log.Printf("dial %s: %v", target, err)
		p.stats.Failed.Add(1)
		return
	}
	defer out.Close()

	timeout := time.Duration(p.cfg.HandshakeTimeoutMs) * time.Millisecond
	select {
	case <-cs.done:
		if cs.doneErr != nil {
			log.Printf("bypass failed: %v", cs.doneErr)
			p.inj.remove(cs)
			p.stats.BypassFailed.Add(1)
			p.stats.Failed.Add(1)
			return
		}
	case <-time.After(timeout):
		cs.finish(ErrBypassTimeout)
		p.inj.remove(cs)
		log.Printf("bypass timeout for %s:%d", p.cfg.ConnectIP, p.cfg.ConnectPort)
		p.stats.BypassFailed.Add(1)
		p.stats.Failed.Add(1)
		return
	}
	p.inj.remove(cs)

	if tcpOut, ok := out.(*net.TCPConn); ok {
		_ = tcpOut.SetNoDelay(true)
	}

	p.stats.Succeeded.Add(1)

	clientToServer := func() error {
		if p.cfg.FragmentEnable {
			if err := p.fragmentFirstRecord(in, out); err != nil {
				return err
			}
		}
		n, err := io.Copy(out, in)
		p.stats.BytesOut.Add(n)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	}

	done := make(chan struct{}, 2)
	go func() {
		_ = clientToServer()
		if tcpOut, ok := out.(*net.TCPConn); ok {
			_ = tcpOut.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(in, out)
		p.stats.BytesIn.Add(n)
		_ = in.CloseWrite()
		done <- struct{}{}
	}()
	<-done
}

// fragmentFirstRecord reads the first TLS record from in and forwards it to
// out in several small segments. Subsequent data flows via plain io.Copy.
func (p *Proxy) fragmentFirstRecord(in net.Conn, out net.Conn) error {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(in, hdr); err != nil {
		return err
	}
	if hdr[0] != 0x16 {
		if _, err := out.Write(hdr); err != nil {
			return err
		}
		return nil
	}
	recLen := int(hdr[3])<<8 | int(hdr[4])
	body := make([]byte, recLen)
	if _, err := io.ReadFull(in, body); err != nil {
		return err
	}
	full := append(hdr, body...)

	lo := p.cfg.FragmentSizeMin
	hi := p.cfg.FragmentSizeMax
	if lo < 1 {
		lo = 1
	}
	if hi < lo {
		hi = lo
	}

	first := lo + randIntn(hi-lo+1)
	if first >= len(full) {
		first = len(full) / 2
		if first < 1 {
			first = 1
		}
	}

	chunks := [][]byte{full[:first]}
	rest := full[first:]
	for len(rest) > 0 {
		// Remaining segments use random sizes between 8 and 64, enough to look
		// like natural MTU-bounded fragments without being tiny.
		size := 8 + randIntn(57)
		if size > len(rest) {
			size = len(rest)
		}
		chunks = append(chunks, rest[:size])
		rest = rest[size:]
	}

	for i, ch := range chunks {
		if _, err := out.Write(ch); err != nil {
			return err
		}
		if i+1 < len(chunks) {
			time.Sleep(time.Duration(5+randIntn(15)) * time.Millisecond)
		}
	}
	return nil
}
