//go:build linux

package main

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"syscall"
	"time"

	nfqueue "github.com/florianl/go-nfqueue"
	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"golang.org/x/sys/unix"
)

// Injector owns the NFQUEUE socket, a raw TCP socket for packet injection,
// and the live connection table.
type Injector struct {
	cfg   *Config
	nfq   *nfqueue.Nfqueue
	raw   int
	rawMu sync.Mutex
	conns sync.Map // connKey -> *connState
}

func NewInjector(cfg *Config) (*Injector, error) {
	nfcfg := nfqueue.Config{
		NfQueue:      cfg.QueueNum,
		MaxPacketLen: 0xffff,
		MaxQueueLen:  8192,
		AfFamily:     unix.AF_INET,
		Copymode:     nfqueue.NfQnlCopyPacket,
		WriteTimeout: 15 * time.Millisecond,
	}
	nfq, err := nfqueue.Open(&nfcfg)
	if err != nil {
		return nil, err
	}

	// Raw socket with IP_HDRINCL so we build the whole IP+TCP frame ourselves.
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_TCP)
	if err != nil {
		nfq.Close()
		return nil, err
	}
	if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, syscall.IP_HDRINCL, 1); err != nil {
		syscall.Close(fd)
		nfq.Close()
		return nil, err
	}

	return &Injector{cfg: cfg, nfq: nfq, raw: fd}, nil
}

func (inj *Injector) Enabled() bool {
	return true
}

func (inj *Injector) Close() {
	if inj.nfq != nil {
		inj.nfq.Close()
	}
	if inj.raw != 0 {
		syscall.Close(inj.raw)
	}
}

// register inserts a fresh connState in the map. Called by the proxy before
// the kernel issues the SYN, so the NFQUEUE handler always finds it in time.
func (inj *Injector) register(cs *connState) {
	key := connKey{srcIP: cs.srcIP, dstIP: cs.dstIP, srcPort: cs.srcPort, dstPort: cs.dstPort}
	inj.conns.Store(key, cs)
}

func (inj *Injector) remove(cs *connState) {
	key := connKey{srcIP: cs.srcIP, dstIP: cs.dstIP, srcPort: cs.srcPort, dstPort: cs.dstPort}
	cs.mu.Lock()
	cs.closed = true
	cs.mu.Unlock()
	inj.conns.Delete(key)
}

// Run registers the NFQUEUE callback and blocks until ctx is cancelled.
func (inj *Injector) Run(ctx context.Context) error {
	hook := func(a nfqueue.Attribute) int {
		inj.handlePacket(a)
		return 0
	}
	errHook := func(e error) int {
		log.Printf("nfqueue: %v", e)
		return 0
	}
	if err := inj.nfq.RegisterWithErrorFunc(ctx, hook, errHook); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func (inj *Injector) handlePacket(a nfqueue.Attribute) {
	if a.PacketID == nil || a.Payload == nil {
		return
	}
	id := *a.PacketID
	payload := *a.Payload

	pkt := gopacket.NewPacket(payload, layers.LayerTypeIPv4, gopacket.DecodeOptions{NoCopy: true, Lazy: true})
	ipL, _ := pkt.Layer(layers.LayerTypeIPv4).(*layers.IPv4)
	tcpL, _ := pkt.Layer(layers.LayerTypeTCP).(*layers.TCP)
	if ipL == nil || tcpL == nil {
		_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
		return
	}

	var key connKey
	copy(key.srcIP[:], ipL.SrcIP.To4())
	copy(key.dstIP[:], ipL.DstIP.To4())
	key.srcPort = uint16(tcpL.SrcPort)
	key.dstPort = uint16(tcpL.DstPort)

	if v, ok := inj.conns.Load(key); ok {
		inj.handleOutbound(v.(*connState), tcpL, id)
		return
	}
	rev := connKey{srcIP: key.dstIP, dstIP: key.srcIP, srcPort: key.dstPort, dstPort: key.srcPort}
	if v, ok := inj.conns.Load(rev); ok {
		inj.handleInbound(v.(*connState), tcpL, id)
		return
	}
	_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
}

func (inj *Injector) handleOutbound(cs *connState, tcp *layers.TCP, id uint32) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.closed {
		_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
		return
	}

	if tcp.SYN && !tcp.ACK && !tcp.RST && !tcp.FIN && len(tcp.Payload) == 0 {
		cs.synSeq = tcp.Seq
		cs.haveSynSeq = true
		_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
		return
	}

	if tcp.ACK && !tcp.SYN && !tcp.RST && !tcp.FIN && len(tcp.Payload) == 0 {
		if !cs.haveSynSeq || !cs.haveSynAckSeq || cs.fakeSent {
			_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
			return
		}
		if tcp.Seq != cs.synSeq+1 || tcp.Ack != cs.synAckSeq+1 {
			_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
			return
		}
		_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
		cs.fakeSent = true
		go inj.sendFake(cs)
		return
	}

	_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
}

func (inj *Injector) handleInbound(cs *connState, tcp *layers.TCP, id uint32) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.closed {
		_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
		return
	}

	if tcp.SYN && tcp.ACK && !tcp.RST && !tcp.FIN && len(tcp.Payload) == 0 {
		cs.synAckSeq = tcp.Seq
		cs.haveSynAckSeq = true
		_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
		return
	}

	if tcp.ACK && !tcp.SYN && !tcp.RST && !tcp.FIN && len(tcp.Payload) == 0 && cs.fakeSent {
		if tcp.Ack == cs.synSeq+1 {
			_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
			go cs.finish(nil)
			return
		}
	}

	_ = inj.nfq.SetVerdict(id, nfqueue.NfAccept)
}

func (inj *Injector) sendFake(cs *connState) {
	time.Sleep(time.Millisecond)

	cs.mu.Lock()
	if cs.closed {
		cs.mu.Unlock()
		return
	}
	payload := cs.fakePayload
	wrongSeq := cs.synSeq + 1 - uint32(len(payload))
	ackNum := cs.synAckSeq + 1
	srcIP := net.IP{cs.srcIP[0], cs.srcIP[1], cs.srcIP[2], cs.srcIP[3]}
	dstIP := net.IP{cs.dstIP[0], cs.dstIP[1], cs.dstIP[2], cs.dstIP[3]}
	srcPort := cs.srcPort
	dstPort := cs.dstPort
	cs.mu.Unlock()

	frame, err := buildTCPSegment(srcIP, dstIP, srcPort, dstPort, wrongSeq, ackNum, payload)
	if err != nil {
		cs.finish(err)
		return
	}

	var sa syscall.SockaddrInet4
	sa.Port = int(dstPort)
	copy(sa.Addr[:], dstIP.To4())

	inj.rawMu.Lock()
	err = syscall.Sendto(inj.raw, frame, 0, &sa)
	inj.rawMu.Unlock()
	if err != nil {
		cs.finish(err)
	}
}

func buildTCPSegment(srcIP, dstIP net.IP, srcPort, dstPort uint16, seq, ack uint32, payload []byte) ([]byte, error) {
	ip := &layers.IPv4{
		Version:  4,
		IHL:      5,
		TTL:      64,
		Id:       0,
		Protocol: layers.IPProtocolTCP,
		SrcIP:    srcIP.To4(),
		DstIP:    dstIP.To4(),
	}
	tcp := &layers.TCP{
		SrcPort:    layers.TCPPort(srcPort),
		DstPort:    layers.TCPPort(dstPort),
		Seq:        seq,
		Ack:        ack,
		DataOffset: 5,
		PSH:        true,
		ACK:        true,
		Window:     65535,
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

// ErrBypassTimeout is returned when the fake-ack doesn't land in time.
var ErrBypassTimeout = errors.New("bypass handshake timeout")
