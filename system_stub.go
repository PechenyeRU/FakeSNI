//go:build !linux

package main

func setupIptables(cfg *Config) (func(), error) {
	return func() {}, nil
}

func setConntrackLiberal() (func(), error) {
	return func() {}, nil
}
