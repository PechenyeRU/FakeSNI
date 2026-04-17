//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func setupIptables(cfg *Config) (func(), error) {
	q := strconv.Itoa(int(cfg.QueueNum))
	port := strconv.Itoa(cfg.ConnectPort)

	rules := [][]string{
		{"-p", "tcp", "-s", cfg.InterfaceIP, "-d", cfg.ConnectIP, "--dport", port,
			"-j", "NFQUEUE", "--queue-num", q, "--queue-bypass"},
		{"-p", "tcp", "-s", cfg.ConnectIP, "--sport", port, "-d", cfg.InterfaceIP,
			"-j", "NFQUEUE", "--queue-num", q, "--queue-bypass"},
	}
	chains := []string{"OUTPUT", "INPUT"}

	for i, r := range rules {
		args := append([]string{"-I", chains[i]}, r...)
		if out, err := exec.Command("iptables", args...).CombinedOutput(); err != nil {
			for j := 0; j < i; j++ {
				del := append([]string{"-D", chains[j]}, rules[j]...)
				_ = exec.Command("iptables", del...).Run()
			}
			return nil, fmt.Errorf("iptables: %v: %s", err, out)
		}
	}

	cleanup := func() {
		for i, r := range rules {
			del := append([]string{"-D", chains[i]}, r...)
			_ = exec.Command("iptables", del...).Run()
		}
	}
	return cleanup, nil
}

func setConntrackLiberal() (func(), error) {
	const path = "/proc/sys/net/netfilter/nf_conntrack_tcp_be_liberal"
	orig, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte("1"), 0644); err != nil {
		return nil, err
	}
	return func() { _ = os.WriteFile(path, orig, 0644) }, nil
}
