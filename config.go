package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
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
	return c, nil
}

// applyEnv overlays FAKESNI_<KEY> environment variables onto c. A set env var
// always wins over the file value.
func applyEnv(c *Config) error {
	setStr("FAKESNI_LISTEN_HOST", &c.ListenHost)
	setStr("FAKESNI_CONNECT_IP", &c.ConnectIP)
	setStr("FAKESNI_WHITE_SNI", &c.WhiteSNI)
	setStr("FAKESNI_INTERFACE_IP", &c.InterfaceIP)
	ints := []struct {
		key string
		dst *int
	}{
		{"FAKESNI_LISTEN_PORT", &c.ListenPort},
		{"FAKESNI_CONNECT_PORT", &c.ConnectPort},
		{"FAKESNI_DECOY_DELAY_MS", &c.DecoyDelayMs},
		{"FAKESNI_DECOY_REFRESH_KB", &c.DecoyRefreshKB},
		{"FAKESNI_HANDSHAKE_TIMEOUT_MS", &c.HandshakeTimeoutMs},
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
