package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// Stats tracks runtime counters exposed over a local HTTP endpoint.
type Stats struct {
	Active       atomic.Int64
	Succeeded    atomic.Int64
	Failed       atomic.Int64
	BypassFailed atomic.Int64
	BytesOut     atomic.Int64
	BytesIn      atomic.Int64
	StartedAt    time.Time
}

func NewStats() *Stats {
	return &Stats{StartedAt: time.Now()}
}

type statsSnapshot struct {
	Active        int64 `json:"active"`
	Succeeded     int64 `json:"succeeded"`
	Failed        int64 `json:"failed"`
	BypassFailed  int64 `json:"bypass_failed"`
	BytesOut      int64 `json:"bytes_out"`
	BytesIn       int64 `json:"bytes_in"`
	UptimeSeconds int64 `json:"uptime_seconds"`
}

func (s *Stats) snapshot() statsSnapshot {
	return statsSnapshot{
		Active:        s.Active.Load(),
		Succeeded:     s.Succeeded.Load(),
		Failed:        s.Failed.Load(),
		BypassFailed:  s.BypassFailed.Load(),
		BytesOut:      s.BytesOut.Load(),
		BytesIn:       s.BytesIn.Load(),
		UptimeSeconds: int64(time.Since(s.StartedAt).Seconds()),
	}
}

// Serve starts a local HTTP server that returns a JSON snapshot on any GET.
func (s *Stats) Serve(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.snapshot())
	})
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return srv.Serve(ln)
}
