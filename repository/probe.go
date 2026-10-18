package repository

import (
	"context"
	"fmt"
)

// Pinger is the optional liveness check a Store may implement. Both shipped
// backends do; the database panel's health endpoint uses it to tell "configured"
// apart from "actually answering".
type Pinger interface {
	Ping(ctx context.Context) error
}

// PingStore checks a store is answering, when its backend supports it. A store
// that implements no Pinger reports nil: there is nothing to check, which is not
// a failure.
func PingStore(ctx context.Context, s Store) error {
	if p, ok := s.(Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

// ProbeResult describes what a candidate database can do, so the panel can warn
// before an operator commits to it rather than after.
type ProbeResult struct {
	Driver string `json:"driver"`
	// Server identifies the server the probe reached ("PostgreSQL 16.2", a
	// sqlite file path), for the panel to echo back as confirmation.
	Server string `json:"server,omitempty"`
	// Writable is whether the probe could actually create and drop an object.
	// A read-only connection opens fine and then fails on the first save, so the
	// check belongs here.
	Writable bool `json:"writable"`
	// Vector reports whether vector similarity search is natively indexed on
	// this database (sqlite-vec, pgvector). When false, vector memory stores
	// still work — the backend falls back to an exact in-process scan — but
	// large stores will be slow, which the panel should say.
	Vector bool `json:"vector"`
	// Notes carries human-readable findings for the panel to show verbatim
	// (missing extension, degraded mode, …).
	Notes []string `json:"notes,omitempty"`
}

// Prober inspects a candidate data source without taking the application onto
// it. It must not create application tables: an operator who mistypes a URI and
// probes someone else's database should leave no trace in it.
type Prober func(ctx context.Context, source string) (ProbeResult, error)

var probers = map[string]Prober{}

// RegisterProber attaches a probe to a driver. Backends call this from init()
// alongside Register.
func RegisterProber(driver string, p Prober) {
	if p == nil {
		panic("repository: RegisterProber prober is nil")
	}
	probers[driver] = p
}

// Probe inspects a candidate database. Drivers without a registered prober fall
// back to "open it and ping it", which answers the only question that always
// matters — can this process reach it — at the cost of applying that driver's
// schema.
func Probe(ctx context.Context, cfg Config) (ProbeResult, error) {
	cfg = cfg.Normalized()
	if err := cfg.Validate(); err != nil {
		return ProbeResult{Driver: cfg.Driver}, err
	}
	if p, ok := probers[cfg.Driver]; ok {
		return p(ctx, cfg.Source)
	}
	st, err := Open(cfg.Driver, cfg.Source)
	if err != nil {
		return ProbeResult{Driver: cfg.Driver}, err
	}
	defer st.Close()
	if err := PingStore(ctx, st); err != nil {
		return ProbeResult{Driver: cfg.Driver}, fmt.Errorf("ping: %w", err)
	}
	return ProbeResult{
		Driver:   cfg.Driver,
		Writable: true,
		Notes:    []string{"driver has no dedicated probe; opened the database to check it"},
	}, nil
}

// HasDriver reports whether a backend is registered (its package imported).
func HasDriver(driver string) bool {
	_, ok := openers[driver]
	return ok
}
