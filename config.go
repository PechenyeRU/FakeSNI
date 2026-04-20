package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config mirrors the JSON config file.
type Config struct {
	ListenHost         string   `json:"LISTEN_HOST"`
	ListenPort         int      `json:"LISTEN_PORT"`
	ConnectIP          string   `json:"CONNECT_IP"`
	ConnectPort        int      `json:"CONNECT_PORT"`
	FakeSNI            string   `json:"FAKE_SNI"`
	SNIPool            []string `json:"SNI_POOL"`
	SNIStrategy        string   `json:"SNI_STRATEGY"`
	BypassStrategy     string   `json:"BYPASS_STRATEGY"`
	LowTTLValue        int      `json:"LOW_TTL_VALUE"`
	FragmentEnable     bool     `json:"FRAGMENT_CLIENT_HELLO"`
	FragmentSizeMin    int      `json:"FRAGMENT_SIZE_MIN"`
	FragmentSizeMax    int      `json:"FRAGMENT_SIZE_MAX"`
	InterfaceIP        string   `json:"INTERFACE_IP"`
	QueueNum           uint16   `json:"QUEUE_NUM"`
	HandshakeTimeoutMs int      `json:"HANDSHAKE_TIMEOUT_MS"`
	NoIptablesSetup    bool     `json:"NO_IPTABLES_SETUP"`
	NoConntrackTweak   bool     `json:"NO_CONNTRACK_TWEAK"`
	LogLevel           string   `json:"LOG_LEVEL"`
	LogFile            string   `json:"LOG_FILE"`
	StatsAddr          string   `json:"STATS_ADDR"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	if c.ListenHost == "" || c.ListenPort == 0 || c.ConnectIP == "" || c.ConnectPort == 0 {
		return nil, fmt.Errorf("config missing required fields: LISTEN_HOST, LISTEN_PORT, CONNECT_IP, CONNECT_PORT")
	}
	if c.FakeSNI == "" && len(c.SNIPool) == 0 {
		return nil, fmt.Errorf("config must set FAKE_SNI or SNI_POOL")
	}
	if len(c.SNIPool) == 0 && c.FakeSNI != "" {
		c.SNIPool = []string{c.FakeSNI}
	}
	if c.SNIStrategy == "" {
		c.SNIStrategy = "sticky_per_connection"
	}
	if c.BypassStrategy == "" {
		c.BypassStrategy = "wrong_seq"
	}
	if c.LowTTLValue == 0 {
		c.LowTTLValue = 8
	}
	if c.FragmentSizeMin == 0 {
		c.FragmentSizeMin = 1
	}
	if c.FragmentSizeMax == 0 {
		c.FragmentSizeMax = 50
	}
	if c.QueueNum == 0 {
		c.QueueNum = 100
	}
	if c.HandshakeTimeoutMs == 0 {
		c.HandshakeTimeoutMs = 2000
	}
	if c.LogLevel == "" {
		c.LogLevel = "INFO"
	}
	if c.StatsAddr == "" {
		c.StatsAddr = "127.0.0.1:9999"
	}
	return c, nil
}
