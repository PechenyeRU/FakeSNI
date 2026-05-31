package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
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

	if os.Geteuid() != 0 {
		log.Fatal("must run as root (raw sockets)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inj, err := NewInjector(cfg)
	if err != nil {
		log.Fatalf("injector: %v", err)
	}
	defer inj.Close()

	done := make(chan struct{})
	go inj.Run(done)
	defer close(done)

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

// detectOutboundIP asks the kernel which local address would be used to reach
// the given remote, without actually sending anything.
func detectOutboundIP(remote string) (string, error) {
	c, err := net.Dial("udp", net.JoinHostPort(remote, "1"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}
