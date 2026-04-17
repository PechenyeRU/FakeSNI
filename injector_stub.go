//go:build !linux

package main

import (
	"context"
	"errors"
)

type Injector struct{}

func NewInjector(cfg *Config) (*Injector, error) {
	return &Injector{}, nil
}

func (inj *Injector) Enabled() bool {
	return false
}

func (inj *Injector) Close() {}

func (inj *Injector) register(cs *connState) {}

func (inj *Injector) remove(cs *connState) {}

func (inj *Injector) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// ErrBypassTimeout is defined on all platforms so shared proxy code builds.
var ErrBypassTimeout = errors.New("bypass handshake timeout")
