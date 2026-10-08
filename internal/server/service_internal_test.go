// Package server verifies the parts of the service that its exported interface
// deliberately hides: the HTTP server New builds, the routes it serves, and the
// listen address it resolves.
package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewConfiguresHTTPServer pins the production HTTP server settings New
// applies. They are not reachable from outside the package, so this is where
// the timeouts and the header limit are asserted.
func TestNewConfiguresHTTPServer(t *testing.T) {
	t.Parallel()

	cfg := NewConfig()
	cfg.Port = ":18090"

	svc := New(cfg)

	if svc.Hub() == nil {
		t.Fatal("New returned a service without a hub")
	}
	if svc.httpServer.Handler == nil {
		t.Fatal("New returned a service without routes")
	}
	if svc.httpServer.Addr != cfg.Port {
		t.Errorf("Expected server addr %s, got %s", cfg.Port, svc.httpServer.Addr)
	}

	timeouts := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"ReadTimeout", svc.httpServer.ReadTimeout, 15 * time.Second},
		{"ReadHeaderTimeout", svc.httpServer.ReadHeaderTimeout, 5 * time.Second},
		{"WriteTimeout", svc.httpServer.WriteTimeout, 15 * time.Second},
		{"IdleTimeout", svc.httpServer.IdleTimeout, 60 * time.Second},
	}
	for _, tt := range timeouts {
		if tt.got != tt.want {
			t.Errorf("Expected %s %v, got %v", tt.name, tt.want, tt.got)
		}
	}

	if svc.httpServer.MaxHeaderBytes != 1<<16 {
		t.Errorf("Expected MaxHeaderBytes %d, got %d", 1<<16, svc.httpServer.MaxHeaderBytes)
	}
}

// TestNewServesTheApplicationRoutes verifies that the handler New builds answers
// the health route, so the service is wired to the real mux rather than an empty
// one.
func TestNewServesTheApplicationRoutes(t *testing.T) {
	t.Parallel()

	svc := New(NewConfig())

	rec := httptest.NewRecorder()
	svc.httpServer.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("Expected status %d from /, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.String() != HealthResponse {
		t.Errorf("Expected body %q, got %q", HealthResponse, rec.Body.String())
	}
}

// TestProductionTimeoutsAllowSlowResponses verifies that a response slower than
// a moment still completes under the timeouts New applies, so the production
// write timeout is not cutting responses off early.
func TestProductionTimeoutsAllowSlowResponses(t *testing.T) {
	t.Parallel()

	// A handler slower than any synchronization delay in this suite. The sleep
	// is the behavior under test, not a wait for something else to happen.
	const handlerDelay = 2 * time.Second

	production := New(NewConfig()).httpServer

	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(handlerDelay)
		w.WriteHeader(http.StatusOK)
	})

	testServer := httptest.NewUnstartedServer(mux)
	testServer.Config.ReadTimeout = production.ReadTimeout
	testServer.Config.WriteTimeout = production.WriteTimeout
	testServer.Config.IdleTimeout = production.IdleTimeout
	testServer.Start()
	t.Cleanup(testServer.Close)

	client := testServer.Client()
	client.Timeout = handlerDelay + 5*time.Second

	resp, err := client.Get(testServer.URL + "/slow")
	if err != nil {
		t.Fatalf("Slow request failed under the production timeouts: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}
}

// TestNewListensOnTheResolvedPort pins the contract documented in
// docs/reference/configuration.md: SERVER_PORT=9000 and SERVER_PORT=:9000 are
// equivalent. New must take the port the hub resolved, not the caller's raw
// value, because http.Server.Addr rejects a bare port.
func TestNewListensOnTheResolvedPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		port string
		want string
	}{
		{"bare port gains a colon", "18091", ":18091"},
		{"already qualified is left alone", ":18092", ":18092"},
		{"host and port are left alone", "127.0.0.1:18093", "127.0.0.1:18093"},
		{"empty falls back to the default", "", defaultPort},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := NewConfig()
			cfg.Port = tt.port

			if got := New(cfg).httpServer.Addr; got != tt.want {
				t.Errorf("Expected addr %q for port %q, got %q", tt.want, tt.port, got)
			}
		})
	}
}

// TestRunDrainsTheHubWhenTheListenerFails pins that a listener failure does not
// leave the hub's goroutines running behind it, and that the listen error stays
// reachable through the error Run returns.
func TestRunDrainsTheHubWhenTheListenerFails(t *testing.T) {
	t.Parallel()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to occupy a port: %v", err)
	}
	t.Cleanup(func() { _ = occupied.Close() })

	cfg := NewConfig()
	cfg.Port = occupied.Addr().String()
	svc := New(cfg)

	// A deadline, so a regression that lets the listen succeed fails here instead
	// of blocking until the test binary times out.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runErr := svc.Run(ctx)

	if runErr == nil {
		t.Fatal("Expected Run to fail on an occupied port")
	}
	if !strings.HasPrefix(runErr.Error(), "http server: listen and serve: ") {
		t.Errorf("Unexpected error text %q", runErr)
	}
	if _, ok := errors.AsType[*net.OpError](runErr); !ok {
		t.Errorf("Expected the listen error to stay reachable, got %v", runErr)
	}
	if !svc.Hub().IsStopped() {
		t.Error("Expected the hub to be stopped after the listener failed")
	}
}

// TestServeDrainsTheHubWhenTheListenerFails pins that Serve runs Run's
// lifecycle rather than a copy of it: a listener that fails — here, one already
// closed — drains the hub Serve started instead of leaving it running, and the
// accept error stays reachable through the error Serve returns.
func TestServeDrainsTheHubWhenTheListenerFails(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	_ = ln.Close()

	svc := New(nil)

	// A deadline, so a regression that keeps serving fails here instead of
	// blocking until the test binary times out.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serveErr := svc.Serve(ctx, ln)

	if serveErr == nil {
		t.Fatal("Expected Serve to fail on a closed listener")
	}
	if !strings.HasPrefix(serveErr.Error(), "http server: serve: ") {
		t.Errorf("Unexpected error text %q", serveErr)
	}
	if !errors.Is(serveErr, net.ErrClosed) {
		t.Errorf("Expected the accept error to stay reachable, got %v", serveErr)
	}
	if !svc.Hub().IsStopped() {
		t.Error("Expected the hub to be stopped after the listener failed")
	}
}

// closeRecordingListener records whether anything has closed it, so a test can
// check that a listener is closed at a given moment rather than eventually.
type closeRecordingListener struct {
	net.Listener
	closed atomic.Bool
}

func (l *closeRecordingListener) Close() error {
	l.closed.Store(true)
	return l.Listener.Close()
}

// TestServeClosesItsListenerBeforeReturning pins the promise in Serve's doc
// comment: ln is closed by the time Serve returns. The case that broke it is a
// context already cancelled when Serve starts: the drain can then finish before
// the serving goroutine has reached http.Server.Serve, so Shutdown has no
// listener to close, and the goroutine closes it only after Serve has returned.
// That goroutine is scheduled at random, so the test runs the case many times.
//
// It reaches Run only through the lifecycle Run shares with Serve: Run's own
// listener is opened inside ListenAndServe, where a test cannot observe it.
func TestServeClosesItsListenerBeforeReturning(t *testing.T) {
	t.Parallel()

	const rounds = 200

	for range rounds {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Failed to listen: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		rec := &closeRecordingListener{Listener: ln}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := New(nil).Serve(ctx, rec); err != nil {
			t.Fatalf("Serve returned %v after a cancelled context, want nil", err)
		}
		if !rec.closed.Load() {
			t.Fatal("Serve returned with its listener still open")
		}
	}
}
