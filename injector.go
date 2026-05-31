package main

import (
	"encoding/binary"
	"errors"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"golang.org/x/sys/unix"
)

// debug enables verbose tracing when FAKESNI_DEBUG is set.
var debug = os.Getenv("FAKESNI_DEBUG") != ""

// ErrBypassTimeout is returned when the SYN-ACK isn't observed in time.
var ErrBypassTimeout = errors.New("bypass timeout: SYN-ACK not observed")

// synInfo is derived from the inbound SYN-ACK: its ack field is our SYN seq + 1
// (where the ClientHello starts), its seq is the server's ISN, and srvTTL is the
// SYN-ACK's IP TTL (used to auto-derive the decoy TTL: hops to server).
type synInfo struct {
	ourSeqP1 uint32
	srvSeq   uint32
	srvTTL   uint8
}

// Injector watches inbound SYN-ACKs for the kernel's upstream connections and
// raw-injects a white-SNI decoy ClientHello at the first data byte, carrying a
// TCP-MD5 option. The firewall reads the decoy SNI and whitens the flow; the
// server drops the decoy (MD5 on a non-MD5 connection, RFC 2385) and keeps the
// kernel's real ClientHello. The decoy MUST be a genuine ClientHello - a minimal
// hand-built one makes the server silently drop the whole flow.
type Injector struct {
	cfg      *Config
	rawSend  int
	rawRecv  int
	rawMu    sync.Mutex
	decoy    []byte
	ifaceIP  [4]byte
	dstIP    [4]byte
	decoyMD5 bool  // tag the decoy so the server drops it (md5 mode)
	decoyTTL uint8 // ip ttl for decoy segments

	syn sync.Map // uint16(srcPort) -> synInfo
}

func NewInjector(cfg *Config) (*Injector, error) {
	send, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_TCP)
	if err != nil {
		return nil, err
	}
	if err := syscall.SetsockoptInt(send, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1); err != nil {
		syscall.Close(send)
		return nil, err
	}
	recv, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_TCP)
	if err != nil {
		syscall.Close(send)
		return nil, err
	}
	tv := syscall.Timeval{Sec: 0, Usec: 200000}
	_ = syscall.SetsockoptTimeval(recv, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	inj := &Injector{cfg: cfg, rawSend: send, rawRecv: recv, decoy: BuildClientHello(cfg.WhiteSNI)}
	copy(inj.ifaceIP[:], net.ParseIP(cfg.InterfaceIP).To4())
	copy(inj.dstIP[:], net.ParseIP(cfg.ConnectIP).To4())
	switch cfg.DecoyMode {
	case "ttl":
		// valid packet that expires in transit before the server (no md5 needed)
		inj.decoyMD5 = false
		switch {
		case cfg.DecoyTTL > 0:
			inj.decoyTTL = uint8(cfg.DecoyTTL)
			log.Printf("decoy mode: ttl (ttl=%d)", cfg.DecoyTTL)
		default:
			// auto: measure the forward hop count to the server and stop a few
			// hops short of it (past the firewall, before the server).
			if hops, err := forwardHops(send, inj.ifaceIP, inj.dstIP, uint16(cfg.ConnectPort)); err == nil {
				t := hops - cfg.DecoyAutoTTLDelta
				if t < 1 {
					t = 1
				}
				inj.decoyTTL = uint8(t)
				log.Printf("decoy mode: ttl (auto: %d hops -> ttl %d)", hops, t)
			} else {
				// fall back to a per-connection estimate from the SYN-ACK ttl
				inj.decoyTTL = 0
				log.Printf("decoy mode: ttl (auto per-connection; forward probe failed: %v)", err)
			}
		}
	default:
		// md5: reaches the server, which drops it (md5 on a non-md5 socket)
		inj.decoyMD5, inj.decoyTTL = true, 64
		log.Printf("decoy mode: md5")
	}

	// Filter the recv socket in-kernel to only SYN-ACKs from the server, so bulk
	// inbound traffic (downloads) doesn't wake userspace. Best-effort: parseSynAck
	// re-checks every packet, so an unfiltered fallback is still correct.
	if err := attachSynAckFilter(recv, inj.dstIP, uint16(cfg.ConnectPort)); err != nil {
		log.Printf("bpf filter not attached (continuing unfiltered): %v", err)
	}
	log.Printf("white decoy ClientHello: %d bytes", len(inj.decoy))
	return inj, nil
}

// attachSynAckFilter installs a classic BPF program on the raw recv socket that
// accepts only TCP SYN-ACK segments coming from dstIP:port. The socket delivers
// the IPv4 header first, so offsets are relative to the start of the IP header.
func attachSynAckFilter(fd int, dstIP [4]byte, port uint16) error {
	srcIP := binary.BigEndian.Uint32(dstIP[:])
	prog := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 12},                          // 0: A = IP src
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 7, K: srcIP},        // 1: src == dstIP?
		{Code: unix.BPF_LDX | unix.BPF_B | unix.BPF_MSH, K: 0},                          // 2: X = IP header len
		{Code: unix.BPF_LD | unix.BPF_H | unix.BPF_IND, K: 0},                           // 3: A = TCP src port
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 4, K: uint32(port)}, // 4: == port?
		{Code: unix.BPF_LD | unix.BPF_B | unix.BPF_IND, K: 13},                          // 5: A = TCP flags
		{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: 0x12},                       // 6: A &= SYN|ACK
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: 0x12},         // 7: == SYN|ACK?
		{Code: unix.BPF_RET | unix.BPF_K, K: 0x40000},                                   // 8: accept
		{Code: unix.BPF_RET | unix.BPF_K, K: 0},                                         // 9: drop
	}
	fprog := &unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}
	return unix.SetsockoptSockFprog(fd, unix.SOL_SOCKET, unix.SO_ATTACH_FILTER, fprog)
}

// forwardHops measures the router-hop distance to dst:port by raw-sending TCP SYN
// probes with increasing TTL (each from a distinct source port) and finding the
// smallest TTL whose probe reaches the server - i.e. comes back as a SYN-ACK or
// RST. This is the forward path length, which (unlike the SYN-ACK's own TTL) is
// not skewed by asymmetric anycast return routing. Returns an error if the server
// never answers (probes filtered).
func forwardHops(send int, srcIP, dstIP [4]byte, dstPort uint16) (int, error) {
	const base = 50000
	const maxTTL = 30
	r, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_TCP)
	if err != nil {
		return 0, err
	}
	defer syscall.Close(r)
	tv := syscall.Timeval{Sec: 0, Usec: 200000}
	_ = syscall.SetsockoptTimeval(r, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	sa := syscall.SockaddrInet4{Port: int(dstPort), Addr: dstIP}
	for ttl := 1; ttl <= maxTTL; ttl++ {
		frame, err := buildSeg(srcIP, dstIP, uint16(base+ttl), dstPort, uint32(0x10000+ttl), 0, fSYN, nil, false, uint8(ttl))
		if err != nil {
			continue
		}
		_ = syscall.Sendto(send, frame, 0, &sa)
	}

	best := 0
	buf := make([]byte, 1500)
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		n, _, err := syscall.Recvfrom(r, buf, 0)
		if err != nil || n < 20 {
			continue
		}
		ihl := int(buf[0]&0x0f) * 4
		if n < ihl+20 {
			continue
		}
		if buf[12] != dstIP[0] || buf[13] != dstIP[1] || buf[14] != dstIP[2] || buf[15] != dstIP[3] {
			continue
		}
		tcp := buf[ihl:]
		if binary.BigEndian.Uint16(tcp[0:2]) != dstPort {
			continue
		}
		dport := int(binary.BigEndian.Uint16(tcp[2:4]))
		if dport <= base || dport > base+maxTTL {
			continue
		}
		if flags := tcp[13]; flags&0x12 == 0x12 || flags&0x04 != 0 { // SYN-ACK or RST
			if ttl := dport - base; best == 0 || ttl < best {
				best = ttl
			}
		}
	}
	if best == 0 {
		return 0, errors.New("server did not answer hop probes")
	}
	return best, nil
}

func (inj *Injector) Close() {
	if inj.rawSend != 0 {
		syscall.Close(inj.rawSend)
	}
	if inj.rawRecv != 0 {
		syscall.Close(inj.rawRecv)
	}
}

// Run records the SYN-ACK of each tracked upstream flow.
func (inj *Injector) Run(done <-chan struct{}) {
	buf := make([]byte, 65535)
	for {
		select {
		case <-done:
			return
		default:
		}
		n, _, err := syscall.Recvfrom(inj.rawRecv, buf, 0)
		if err != nil {
			continue
		}
		inj.parseSynAck(buf[:n])
	}
}

func (inj *Injector) parseSynAck(pkt []byte) {
	if len(pkt) < 20 {
		return
	}
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+20 {
		return
	}
	if pkt[12] != inj.dstIP[0] || pkt[13] != inj.dstIP[1] || pkt[14] != inj.dstIP[2] || pkt[15] != inj.dstIP[3] {
		return
	}
	tcp := pkt[ihl:]
	srcPort := binary.BigEndian.Uint16(tcp[0:2])
	if srcPort != uint16(inj.cfg.ConnectPort) {
		return
	}
	if tcp[13]&0x12 != 0x12 { // SYN+ACK
		return
	}
	dstPort := binary.BigEndian.Uint16(tcp[2:4])
	seq := binary.BigEndian.Uint32(tcp[4:8])
	ack := binary.BigEndian.Uint32(tcp[8:12])
	inj.syn.Store(dstPort, synInfo{ourSeqP1: ack, srvSeq: seq, srvTTL: pkt[8]})
}

// WaitSynAck blocks until the connection's SYN-ACK has been observed and returns
// the derived sequence info.
func (inj *Injector) WaitSynAck(srcPort uint16) (synInfo, error) {
	deadline := time.Now().Add(time.Duration(inj.cfg.HandshakeTimeoutMs) * time.Millisecond)
	for {
		if v, ok := inj.syn.LoadAndDelete(srcPort); ok {
			return v.(synInfo), nil
		}
		if time.Now().After(deadline) {
			return synInfo{}, ErrBypassTimeout
		}
		time.Sleep(time.Millisecond)
	}
}

const uMSS = 1388

// ttlFor returns the IP TTL to stamp on the decoy for this connection: the fixed
// value when set, or - in auto mode (DecoyTTL == 0) - the hop count to the server
// derived from the SYN-ACK's TTL, minus the configured delta.
func (inj *Injector) ttlFor(info synInfo) uint8 {
	if inj.decoyTTL > 0 {
		return inj.decoyTTL
	}
	hops := int(initialTTL(info.srvTTL)) - int(info.srvTTL)
	t := hops - inj.cfg.DecoyAutoTTLDelta
	if t < 1 {
		t = 1
	}
	return uint8(t)
}

// initialTTL guesses the sender's original TTL from a received value (common
// defaults are 64, 128, 255).
func initialTTL(recv uint8) uint8 {
	switch {
	case recv > 128:
		return 255
	case recv > 64:
		return 128
	default:
		return 64
	}
}

// InjectDecoy raw-sends the given decoy at the connection's first data byte,
// segmented into MSS-sized pieces tagged per the decoy mode (md5 or short ttl),
// at the same sequence numbers as the real ClientHello, then holds DecoyDelayMs.
func (inj *Injector) InjectDecoy(srcPort uint16, info synInfo, decoy []byte) error {
	ttl := inj.ttlFor(info)
	seq := info.ourSeqP1
	for off := 0; off < len(decoy); off += uMSS {
		end := off + uMSS
		if end > len(decoy) {
			end = len(decoy)
		}
		if err := inj.injectAt(srcPort, seq, info.srvSeq+1, decoy[off:end], ttl); err != nil {
			return err
		}
		seq += uint32(end - off)
	}
	if debug {
		log.Printf("decoy injected: srcPort=%d seq=%d len=%d ttl=%d", srcPort, info.ourSeqP1, len(decoy), ttl)
	}
	time.Sleep(time.Duration(inj.cfg.DecoyDelayMs) * time.Millisecond)
	return nil
}

// inject re-sends the base decoy at the given sequence (single segment, for the
// mid-stream refresh; bounded by MSS).
func (inj *Injector) inject(srcPort uint16, seq, ack uint32, ttl uint8) error {
	d := inj.decoy
	if len(d) > uMSS {
		d = d[:uMSS]
	}
	return inj.injectAt(srcPort, seq, ack, d, ttl)
}

func (inj *Injector) injectAt(srcPort uint16, seq, ack uint32, payload []byte, ttl uint8) error {
	frame, err := buildSeg(inj.ifaceIP, inj.dstIP, srcPort, uint16(inj.cfg.ConnectPort),
		seq, ack, fPSH|fACK, payload, inj.decoyMD5, ttl)
	if err != nil {
		return err
	}
	var sa syscall.SockaddrInet4
	sa.Port = inj.cfg.ConnectPort
	sa.Addr = inj.dstIP
	inj.rawMu.Lock()
	err = syscall.Sendto(inj.rawSend, frame, 0, &sa)
	inj.rawMu.Unlock()
	return err
}

const (
	fFIN = 0x01
	fSYN = 0x02
	fRST = 0x04
	fPSH = 0x08
	fACK = 0x10
)

// buildSeg serializes an IPv4+TCP segment with the given TTL, optionally with a
// TCP-MD5 option.
func buildSeg(src, dst [4]byte, sport, dport uint16, seq, ack uint32, flags uint8, payload []byte, md5 bool, ttl uint8) ([]byte, error) {
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      ttl,
		Protocol: layers.IPProtocolTCP,
		SrcIP:    net.IP(src[:]),
		DstIP:    net.IP(dst[:]),
	}
	tcp := &layers.TCP{
		SrcPort: layers.TCPPort(sport),
		DstPort: layers.TCPPort(dport),
		Seq:     seq,
		Ack:     ack,
		Window:  65535,
		SYN:     flags&fSYN != 0,
		ACK:     flags&fACK != 0,
		PSH:     flags&fPSH != 0,
		FIN:     flags&fFIN != 0,
		RST:     flags&fRST != 0,
	}
	if md5 {
		tcp.Options = []layers.TCPOption{{OptionType: 19, OptionLength: 18, OptionData: make([]byte, 16)}}
	}
	if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
		return nil, err
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, ip, tcp, gopacket.Payload(payload)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
