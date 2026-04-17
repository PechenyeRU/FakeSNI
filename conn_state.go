package main

import "sync"

// connKey uniquely identifies a tracked outbound 4-tuple.
type connKey struct {
	srcIP    [4]byte
	dstIP    [4]byte
	srcPort  uint16
	dstPort  uint16
}

// connState holds per-connection bypass state shared between proxy and injector.
type connState struct {
	srcIP, dstIP     [4]byte
	srcPort, dstPort uint16

	mu            sync.Mutex
	synSeq        uint32
	synAckSeq     uint32
	haveSynSeq    bool
	haveSynAckSeq bool
	fakeSent      bool
	closed        bool

	fakePayload []byte

	done     chan struct{}
	doneErr  error
	doneOnce sync.Once
}

func (cs *connState) finish(err error) {
	cs.doneOnce.Do(func() {
		cs.doneErr = err
		close(cs.done)
	})
}
