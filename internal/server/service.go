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

// Shutdown budget: the HTTP server and the hub each get half of the total.
const (
	shutdownTimeout = 30 * time.Second
	stageTimeout    = shutdownTimeout / 2
)

// Service is the running server: one hub, the routes bound to it, and the HTTP
// server that serves them. Constructing it and running it, with [Service.Run] or
// [Service.Serve], are the only things a caller does, so the shutdown ordering
// cannot be got wrong from outside.
type Service struct {
	hub        *Hub
	httpServer *http.Server
}

// New assembles the service described by cfg without starting anything.
//
// cfg is handed to the hub, which resolves it and owns it from then on, so the
// connection paths (origin checks, message size limits, rate limiting) read this
// service's settings rather than the process's.
func New(cfg *Config) *Service {
	hub := newHub(cfg)

	return &Service{
		hub: hub,
		httpServer: &http.Server{
			// The hub's resolved port, not the caller's: resolveConfig supplies
			// the default and rewrites a bare port into ":port", which is the
			// form http.Server.Addr requires.
			Addr:              hub.cfg.Port,
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

// Run starts the hub and serves HTTP on the configured port until the listener
// fails or ctx is done. A listener failure drains the HTTP server and the hub,
// so nothing outlives the call, and is returned wrapped, joined with the
// drain's error if that overran its budget. Cancelling ctx drains the service
// and returns nil once it has stopped — the listener included — or the drain's
// error if a stage overran its budget.
func (s *Service) Run(ctx context.Context) error {
	return s.run(ctx, s.httpServer.Addr, func() error {
		if err := s.httpServer.ListenAndServe(); err != nil {
			return fmt.Errorf("listen and serve: %w", err)
		}
		return nil
	})
}

// Serve is [Service.Run] on a listener the caller has already opened, in place
// of the configured port; it closes ln before returning. It exists for a caller
// that needs the address before the service is built — a test listening on an
// ephemeral port, whose own origin has to be on the allow-list — and runs the
// same lifecycle, drain included.
func (s *Service) Serve(ctx context.Context, ln net.Listener) error {
	return s.run(ctx, ln.Addr().String(), func() error {
		if err := s.httpServer.Serve(ln); err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	})
}

// run is the lifecycle [Service.Run] and [Service.Serve] share: start the hub,
// serve on serve's goroutine, and drain on cancellation or a listener failure.
// addr is only logged.
func (s *Service) run(ctx context.Context, addr string, serve func() error) error {
	s.hub.start()
	log().Info("hub started and ready to manage WebSocket connections")

	serverErrors := make(chan error, 1)
	go func() {
		log().Info("server listening", "addr", addr)

		if err := serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
			return
		}

		serverErrors <- nil
	}()

	select {
	case err := <-serverErrors:
		if err != nil {
			// The listener is gone, but connections the server accepted before
			// it failed are still being served, and the hub is still running.
			// Both are drained, in the same order as on cancellation, so
			// nothing outlives the call.
			return errors.Join(fmt.Errorf("http server: %w", err), s.shutdown())
		}
		return nil

	case <-ctx.Done():
		log().Info("shutdown signal received; draining connections")

		shutdownErr := s.shutdown()

		// The serving goroutine may not have reached Serve or ListenAndServe
		// when the drain ran, and Shutdown cannot close a listener it has not
		// seen. Once Shutdown has run, both return ErrServerClosed as soon as
		// they start, closing the listener on the way out, so this wait is
		// short; it is what keeps the listener from outliving the call.
		//
		// The goroutine reports nil for that close. Anything else means the
		// listener failed on its own before the shutdown reached it, and a
		// cancellation that won the select does not make that failure moot.
		serveErr := <-serverErrors

		if shutdownErr != nil {
			shutdownErr = fmt.Errorf("graceful shutdown: %w", shutdownErr)
		}
		if serveErr != nil {
			return errors.Join(fmt.Errorf("http server: %w", serveErr), shutdownErr)
		}
		if shutdownErr != nil {
			return shutdownErr
		}

		log().Info("server stopped gracefully")
		return nil
	}
}

// shutdown stops accepting new connections and then drains the hub, giving up
// once the overall shutdown budget is exhausted.
//
// The HTTP server must stop accepting connections before the hub drains, so the
// two stages run in sequence and their failures are reported together. Each
// stage gets its own half of the budget from a context derived from the overall
// one, which caps the total even if a stage overruns.
func (s *Service) shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	httpErr := withStageDeadline(ctx, s.stopAccepting)
	hubErr := withStageDeadline(ctx, s.hub.shutdown)

	return errors.Join(httpErr, hubErr)
}

// stopAccepting closes the listeners and drains in-flight requests. Upgraded
// WebSocket connections are hijacked, so they are not waited on here — the hub
// closes them in the second stage.
func (s *Service) stopAccepting(ctx context.Context) error {
	log().Info("shutting down HTTP server")

	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown http server: %w", err)
	}

	log().Info("HTTP server shutdown completed")
	return nil
}

// withStageDeadline runs stage under its own slice of the shutdown budget.
func withStageDeadline(parent context.Context, stage func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(parent, stageTimeout)
	defer cancel()

	return stage(ctx)
}
