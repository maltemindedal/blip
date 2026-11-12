// Package server owns the lifecycle of the Blip service: it builds the hub, the
// routes, and the HTTP server that fronts them, runs them until the process is
// asked to stop, and drains them in the one order that is safe.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const shutdownTimeout = 30 * time.Second

// Service is the running server: one hub, the routes bound to it, and the HTTP
// server that serves them. Constructing it and running it, with [Service.Run] or
// [Service.Serve], are the only things a caller does, so the shutdown ordering
// cannot be got wrong from outside.
type Service struct {
	hub        *Hub
	httpServer *http.Server
}

// New resolves cfg, starts the hub that will run under it, and builds the HTTP
// server in front of that hub.
//
// The hub is running when New returns, and [Service.Run] or [Service.Serve] is
// what stops it, on every way out. A Service that is built and never run leaves
// its hub's goroutine behind, so follow every New with one of the two.
func New(cfg *Config) *Service {
	return newService(resolveConfig(cfg))
}

func newService(s settings) *Service {
	hub := startHub(s)
	log().Info("hub started and ready to manage WebSocket connections")

	return &Service{
		hub: hub,
		httpServer: &http.Server{
			Addr:              s.addr,
			Handler:           setupRoutes(hub),
			ReadTimeout:       15 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 16,
		},
	}
}

// Hub returns the hub this service broadcasts through. Nothing in the running
// server needs it; it exists so tests can observe registration and client
// counts against the service they actually started.
func (s *Service) Hub() *Hub {
	return s.hub
}

// Run listens on the configured port and serves there as [Service.Serve] does.
// Failing to listen is one more way for serving to end, not a path of its own:
// the service is drained and the listen error returned.
func (s *Service) Run(ctx context.Context) error {
	addr := s.httpServer.Addr

	return s.serve(ctx, addr, func() (net.Listener, error) { return net.Listen("tcp", addr) })
}

// Serve serves HTTP on ln, which it closes before returning, until ln fails or
// ctx is done.
//
// However serving ends, the HTTP server and then the hub are drained, so
// nothing outlives the call. A listener failure is returned wrapped, joined
// with the drain's error if that overran its budget. Cancelling ctx returns nil
// once the service has stopped, or the drain's error, joined with the
// listener's if it failed on its own in the meantime.
func (s *Service) Serve(ctx context.Context, ln net.Listener) error {
	return s.serve(ctx, ln.Addr().String(), func() (net.Listener, error) { return ln, nil })
}

func (s *Service) serve(ctx context.Context, addr string, listen func() (net.Listener, error)) error {
	return supervise(ctx,
		func() error {
			ln, err := listen()
			if err != nil {
				return fmt.Errorf("http server: %w", err)
			}

			log().Info("server listening", "addr", addr)

			if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("http server: serve: %w", err)
			}
			return nil
		},
		shutdownTimeout,
		s.stopAccepting,
		s.hub.shutdown,
	)
}

// stopAccepting closes the listeners and drains in-flight requests. Upgraded
// WebSocket connections are hijacked, so they are not waited on here — the hub
// closes them in the next stage.
func (s *Service) stopAccepting(ctx context.Context) error {
	log().Info("shutting down HTTP server")

	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}

	log().Info("HTTP server shutdown completed")
	return nil
}
