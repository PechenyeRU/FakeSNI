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
			fragN, err := p.fragmentFirstRecord(in, out)
			p.stats.BytesOut.Add(fragN)
			if err != nil {
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
// out split into 2-4 TCP segments. Returns bytes written to out.
func (p *Proxy) fragmentFirstRecord(in net.Conn, out net.Conn) (int64, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(in, hdr); err != nil {
		return 0, err
	}
	if hdr[0] != 0x16 {
		n, err := out.Write(hdr)
		return int64(n), err
	}
	recLen := int(hdr[3])<<8 | int(hdr[4])
	body := make([]byte, recLen)
	if _, err := io.ReadFull(in, body); err != nil {
		return 0, err
	}
	full := append(hdr, body...)
	total := len(full)

	lo := p.cfg.FragmentSizeMin
	hi := p.cfg.FragmentSizeMax
	if lo < 1 {
		lo = 1
	}
	if hi < lo {
		hi = lo
	}
	if hi > total-1 {
		hi = total - 1
	}

	first := lo
	if hi > lo {
		first = lo + randIntn(hi-lo+1)
	}
	if first < 1 {
		first = 1
	}
	if first >= total {
		first = total / 2
		if first < 1 {
			first = 1
		}
	}

	// Split remainder into one or two additional chunks, giving a total of
	// 2-3 segments. Keep per-chunk delays small (≤5ms) so aggregate latency
	// added to the handshake stays under ~15ms.
	chunks := [][]byte{full[:first]}
	rest := full[first:]
	if len(rest) > 0 {
		if len(rest) > 64 && randIntn(2) == 0 {
			mid := len(rest)/2 + randIntn(len(rest)/4+1)
			chunks = append(chunks, rest[:mid], rest[mid:])
		} else {
			chunks = append(chunks, rest)
		}
	}

	var written int64
	for i, ch := range chunks {
		n, err := out.Write(ch)
		written += int64(n)
		if err != nil {
			return written, err
		}
		if i+1 < len(chunks) {
			time.Sleep(time.Duration(1+randIntn(5)) * time.Millisecond)
		}
	}
	return written, nil
}
