package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config mirrors the JSON config file. Every field can also be set via an
// environment variable named FAKESNI_<JSON_KEY> (e.g. FAKESNI_WHITE_SNI), which
// takes priority over the file. The file itself is optional: if it is missing
// and the required values are supplied via env, that is enough (handy for
// Docker without a mounted config).
type Config struct {
	ListenHost         string `json:"LISTEN_HOST"`
	ListenPort         int    `json:"LISTEN_PORT"`
	ConnectIP          string `json:"CONNECT_IP"`
	ConnectPort        int    `json:"CONNECT_PORT"`
	WhiteSNI           string `json:"WHITE_SNI"`
	DecoyDelayMs       int    `json:"DECOY_DELAY_MS"`
	DecoyRefreshKB     int    `json:"DECOY_REFRESH_KB"`
	InterfaceIP        string `json:"INTERFACE_IP"`
	HandshakeTimeoutMs int    `json:"HANDSHAKE_TIMEOUT_MS"`
	// DecoyMode selects how the decoy is kept away from the real server:
	// "md5" (default) tags it so the server drops it; "ttl" gives it a short
	// TTL so it expires in transit before reaching the server. Use "ttl" on
	// paths where the md5 option does not survive (e.g. behind some home CPEs).
	DecoyMode string `json:"DECOY_MODE"`
	// DecoyTTL is the fixed IP TTL for "ttl" mode. 0 (default) auto-derives it
	// per connection from the SYN-ACK's TTL (hops to server minus the delta).
	DecoyTTL          int `json:"DECOY_TTL"`
	DecoyAutoTTLDelta int `json:"DECOY_AUTOTTL_DELTA"`
}

func LoadConfig(path string) (*Config, error) {
	c := &Config{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := applyEnv(c); err != nil {
		return nil, err
	}
	c.DecoyMode = strings.ToLower(strings.TrimSpace(c.DecoyMode))
	switch c.DecoyMode {
	case "", "md5", "ttl":
	default:
		return nil, fmt.Errorf("DECOY_MODE must be \"md5\" or \"ttl\", got %q", c.DecoyMode)
	}
	if c.ListenHost == "" || c.ListenPort == 0 || c.ConnectIP == "" || c.ConnectPort == 0 || c.WhiteSNI == "" {
		return nil, fmt.Errorf("config missing required fields (need LISTEN_HOST, LISTEN_PORT, CONNECT_IP, CONNECT_PORT, WHITE_SNI) via file or FAKESNI_* env")
	}
	if c.DecoyDelayMs == 0 {
		c.DecoyDelayMs = 6
	}
	if c.DecoyRefreshKB == 0 {
		c.DecoyRefreshKB = 64
	}
	if c.HandshakeTimeoutMs == 0 {
		c.HandshakeTimeoutMs = 2000
	}
	if c.DecoyAutoTTLDelta == 0 {
		c.DecoyAutoTTLDelta = 2
	}
	return c, nil
}

// applyEnv overlays FAKESNI_<KEY> environment variables onto c. A set env var
// always wins over the file value.
func applyEnv(c *Config) error {
	setStr("FAKESNI_LISTEN_HOST", &c.ListenHost)
	setStr("FAKESNI_CONNECT_IP", &c.ConnectIP)
	setStr("FAKESNI_WHITE_SNI", &c.WhiteSNI)
	setStr("FAKESNI_INTERFACE_IP", &c.InterfaceIP)
	setStr("FAKESNI_DECOY_MODE", &c.DecoyMode)
	ints := []struct {
		key string
		dst *int
	}{
		{"FAKESNI_LISTEN_PORT", &c.ListenPort},
		{"FAKESNI_CONNECT_PORT", &c.ConnectPort},
		{"FAKESNI_DECOY_DELAY_MS", &c.DecoyDelayMs},
		{"FAKESNI_DECOY_REFRESH_KB", &c.DecoyRefreshKB},
		{"FAKESNI_HANDSHAKE_TIMEOUT_MS", &c.HandshakeTimeoutMs},
		{"FAKESNI_DECOY_TTL", &c.DecoyTTL},
		{"FAKESNI_DECOY_AUTOTTL_DELTA", &c.DecoyAutoTTLDelta},
	}
	for _, e := range ints {
		if v := os.Getenv(e.key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s: %w", e.key, err)
			}
			*e.dst = n
		}
	}
	return nil
}

func setStr(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}
