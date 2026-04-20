package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Set via -ldflags at release build time.
var (
	Version   = "dev"
	Commit    = "none"
	BuildTime = "unknown"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		log.Printf("fakesni %s (commit %s, built %s)", Version, Commit, BuildTime)
		return
	}

	cfg, err := LoadConfig(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if cfg.LogFile != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err == nil {
			f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				log.SetOutput(io.MultiWriter(os.Stderr, f))
			}
		}
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if cfg.InterfaceIP == "" {
		ip, err := detectOutboundIP(cfg.ConnectIP)
		if err != nil {
			log.Fatalf("detect interface ip: %v", err)
		}
		cfg.InterfaceIP = ip
	}
	log.Printf("interface ip: %s", cfg.InterfaceIP)

	mode, err := parseBypassMode(cfg.BypassStrategy)
	if err != nil {
		log.Fatalf("%v", err)
	}
	sniStrat, err := parseSNIStrategy(cfg.SNIStrategy)
	if err != nil {
		log.Fatalf("%v", err)
	}
	pool := NewSNIPool(cfg.SNIPool, sniStrat)
	log.Printf("bypass=%s sni_strategy=%s sni_pool=%d entries",
		cfg.BypassStrategy, cfg.SNIStrategy, len(cfg.SNIPool))

	if os.Geteuid() != 0 {
		log.Fatal("must run as root (NFQUEUE + raw sockets + /proc/sys writes)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !cfg.NoConntrackTweak {
		if restore, err := setConntrackLiberal(); err != nil {
			log.Printf("warning: could not tweak conntrack: %v", err)
		} else {
			defer restore()
		}
	}

	if !cfg.NoIptablesSetup {
		cleanup, err := setupIptables(cfg)
		if err != nil {
			log.Fatalf("iptables: %v", err)
		}
		defer cleanup()
	}

	inj, err := NewInjector(cfg)
	if err != nil {
		log.Fatalf("injector: %v", err)
	}
	defer inj.Close()

	stats := NewStats()
	go func() {
		if err := stats.Serve(ctx, cfg.StatsAddr); err != nil && err != http.ErrServerClosed {
			log.Printf("stats: %v", err)
		}
	}()

	go func() {
		if err := inj.Run(ctx); err != nil {
			log.Printf("injector stopped: %v", err)
			cancel()
		}
	}()

	prx := NewProxy(cfg, inj, pool, mode, stats)
	go func() {
		if err := prx.Run(ctx); err != nil {
			log.Printf("proxy stopped: %v", err)
			cancel()
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
	case <-ctx.Done():
	}
	log.Println("shutting down")
	cancel()
}

func detectOutboundIP(remote string) (string, error) {
	c, err := net.Dial("udp", net.JoinHostPort(remote, "1"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
