package main

import (
	"context"
	"io"
	"log"
	"net"
	"strconv"
	"syscall"
	"time"
)

// Proxy is a plain TCP forwarder: it accepts clients on LISTEN_HOST:LISTEN_PORT
// and opens a matching outbound connection to CONNECT_IP:CONNECT_PORT. For
// every outbound connection it coordinates with the Injector to run the
// SNI-spoofing bypass before any real bytes are relayed.
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
	log.Printf("listening on %s -> %s:%d (fake SNI: %s)",
		ln.Addr(), p.cfg.ConnectIP, p.cfg.ConnectPort, p.cfg.FakeSNI)

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

	ifaceIP := net.ParseIP(p.cfg.InterfaceIP).To4()
	connectIP := net.ParseIP(p.cfg.ConnectIP).To4()
	if ifaceIP == nil || connectIP == nil {
		log.Printf("invalid ipv4 in config")
		return
	}

	fakeData := BuildClientHello(p.cfg.FakeSNI)

	// The Control callback below runs after bind() and before connect(), so
	// we can learn the ephemeral source port and register the connection
	// with the injector before the kernel emits the SYN.
	var cs *connState
	dialer := net.Dialer{
		LocalAddr: &net.TCPAddr{IP: ifaceIP},
		Control: func(network, address string, rc syscall.RawConn) error {
			if !p.inj.Enabled() {
				return nil
			}
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
		return
	}
	defer out.Close()

	if p.inj.Enabled() {
		// Wait for the injector to confirm the fake ClientHello was absorbed.
		timeout := time.Duration(p.cfg.HandshakeTimeoutMs) * time.Millisecond
		select {
		case <-cs.done:
			if cs.doneErr != nil {
				log.Printf("bypass failed: %v", cs.doneErr)
				p.inj.remove(cs)
				return
			}
		case <-time.After(timeout):
			cs.finish(ErrBypassTimeout)
			p.inj.remove(cs)
			log.Printf("bypass timeout for %s:%d", p.cfg.ConnectIP, p.cfg.ConnectPort)
			return
		}
		// Remove from the tracking map so further packets take the fast path.
		p.inj.remove(cs)
	}

	// Bidirectional relay until either side EOFs or errors.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(out, in); done <- struct{}{} }()
	go func() { _, _ = io.Copy(in, out); done <- struct{}{} }()
	<-done
}
