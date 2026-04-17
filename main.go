package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
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

	if cfg.InterfaceIP == "" {
		ip, err := detectOutboundIP(cfg.ConnectIP)
		if err != nil {
			log.Fatalf("detect interface ip: %v", err)
		}
		cfg.InterfaceIP = ip
	}
	log.Printf("interface ip: %s", cfg.InterfaceIP)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inj, err := NewInjector(cfg)
	if err != nil {
		log.Fatalf("injector: %v", err)
	}
	defer inj.Close()

	if inj.Enabled() {
		if os.Geteuid() != 0 {
			log.Fatal("must run as root (NFQUEUE + raw sockets + /proc/sys writes)")
		}

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
	} else {
		log.Printf("warning: packet bypass is disabled on %s; running as a plain TCP proxy", runtime.GOOS)
	}

	go func() {
		if err := inj.Run(ctx); err != nil {
			log.Printf("injector stopped: %v", err)
			cancel()
		}
	}()

	prx := NewProxy(cfg, inj)
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

// detectOutboundIP asks the kernel which local address would be used to
// reach the given remote, without actually sending anything.
func detectOutboundIP(remote string) (string, error) {
	c, err := net.Dial("udp", net.JoinHostPort(remote, "1"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
