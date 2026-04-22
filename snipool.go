package main

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
)

// SNIStrategy selects how the pool picks a hostname for each connection.
type SNIStrategy int

const (
	SNIRandom SNIStrategy = iota
	SNIRoundRobin
	SNISticky
)

func parseSNIStrategy(s string) (SNIStrategy, error) {
	switch s {
	case "", "random":
		return SNIRandom, nil
	case "round_robin":
		return SNIRoundRobin, nil
	case "sticky_per_connection":
		return SNISticky, nil
	}
	return 0, errors.New("unknown sni strategy: " + s)
}

// SNIPool serves decoy hostnames according to the configured strategy.
type SNIPool struct {
	hosts    []string
	strategy SNIStrategy
	rrIdx    uint64

	mu     sync.Mutex
	sticky map[uint64]string
}

func NewSNIPool(hosts []string, strategy SNIStrategy) *SNIPool {
	if len(hosts) == 0 {
		hosts = []string{"www.digikala.com"}
	}
	return &SNIPool{
		hosts:    hosts,
		strategy: strategy,
		sticky:   make(map[uint64]string),
	}
}

// Next returns a hostname. connID identifies the calling connection and is
// only consulted when the pool is in sticky mode.
func (p *SNIPool) Next(connID uint64) string {
	switch p.strategy {
	case SNIRoundRobin:
		i := atomic.AddUint64(&p.rrIdx, 1) - 1
		return p.hosts[i%uint64(len(p.hosts))]
	case SNISticky:
		p.mu.Lock()
		defer p.mu.Unlock()
		if h, ok := p.sticky[connID]; ok {
			return h
		}
		h := p.hosts[randIntn(len(p.hosts))]
		p.sticky[connID] = h
		return h
	default:
		return p.hosts[randIntn(len(p.hosts))]
	}
}

// Release drops sticky state for a connection once it's closed.
func (p *SNIPool) Release(connID uint64) {
	if p.strategy != SNISticky {
		return
	}
	p.mu.Lock()
	delete(p.sticky, connID)
	p.mu.Unlock()
}

func randIntn(n int) int {
	if n <= 1 {
		return 0
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint64(b[:]) % uint64(n))
}
