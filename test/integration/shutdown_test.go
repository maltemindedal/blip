package integration

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/maltemindedal/blip/internal/server"
	"github.com/maltemindedal/blip/test/testhelpers"
)

const (
	testOriginURL = "http://localhost:8080"

	// shutdownBudget is the deadline every shutdown in this package gets unless
	// it is deliberately testing a short one.
	shutdownBudget = 5 * time.Second
)

// TestGracefulShutdownWithClients verifies that cancelling the service's context
// closes every active client connection and leaves the hub stopped.
func TestGracefulShutdownWithClients(t *testing.T) {
	t.Parallel()

	svc := startService(t, ":18082")

	numClients := 5
	clients := connectTestClients(t, svc.Hub(), numClients, svc.wsURL())

	if err := svc.shutdown(t); err != nil {
		t.Errorf("Service run returned an error: %v", err)
	}

	verifyClientsDisconnected(t, clients, numClients)

	if !svc.Hub().IsStopped() {
		t.Error("Hub did not report stopped after the service shut down")
	}
}

// TestShutdownStopsAcceptingBeforeDrainingClients pins the ordering the service
// exists to guarantee: the HTTP server stops accepting before the hub drains,
// and both stages actually run. The two stages complete microseconds apart, so
// the order is read off the log the service emits rather than raced against
// from outside. Afterwards the port must refuse connections and every client
// must be closed.
//
// This is the one test in the suite that cannot call t.Parallel(): reading the
// order off the log means swapping the logger, and SetLogger is still
// process-wide, so a parallel test would capture records from its neighbours.
func TestShutdownStopsAcceptingBeforeDrainingClients(t *testing.T) {
	const port = ":18085"

	logs := captureServerLogs(t)

	svc := startService(t, port)
	clients := connectTestClients(t, svc.Hub(), 2, svc.wsURL())

	if err := svc.shutdown(t); err != nil {
		t.Errorf("Service run returned an error: %v", err)
	}

	assertLoggedInOrder(t, logs.String(),
		"shutting down HTTP server",
		"HTTP server shutdown completed",
		"initiating hub shutdown",
		"hub shutdown completed",
	)

	verifyClientsDisconnected(t, clients, len(clients))

	// The address the service actually bound, not one that merely resolves to it:
	// a refusal from somewhere else would pass this assertion for free.
	if conn, err := net.DialTimeout("tcp", svc.addr, time.Second); err == nil {
		_ = conn.Close()
		t.Error("HTTP server was still accepting connections after shutdown")
	}
}

// syncBuffer collects log output written from the service's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// captureServerLogs redirects the package logger into a buffer for the duration
// of the test, at the level production runs at.
func captureServerLogs(t *testing.T) *syncBuffer {
	t.Helper()

	logs := &syncBuffer{}
	server.SetLogger(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { server.SetLogger(nil) })

	return logs
}

// assertLoggedInOrder checks that every message appears in output, in the order
// given.
func assertLoggedInOrder(t *testing.T, output string, messages ...string) {
	t.Helper()

	previous := -1
	for _, message := range messages {
		at := strings.Index(output, message)
		if at < 0 {
			t.Fatalf("Expected the shutdown log to contain %q, got:\n%s", message, output)
		}
		if at < previous {
			t.Errorf("Expected %q to be logged after the preceding stage, got:\n%s", message, output)
		}
		previous = at
	}
}

// connectTestClients creates multiple WebSocket clients and returns once the hub
// has registered every one of them.
func connectTestClients(t *testing.T, hub *server.Hub, numClients int, url string) []*websocket.Conn {
	t.Helper()

	clients := make([]*websocket.Conn, numClients)

	for i := range numClients {
		conn, err := testhelpers.ConnectWebSocket(url)
		if err != nil {
			t.Fatalf("Failed to connect client %d: %v", i, err)
		}
		clients[i] = conn
	}

	testhelpers.WaitFor(t, shutdownBudget, "every client to register", func() bool {
		return hub.ClientCount() == numClients
	})

	return clients
}

// verifyClientsDisconnected checks that all client connections are closed
func verifyClientsDisconnected(t *testing.T, clients []*websocket.Conn, expectedCount int) {
	t.Helper()

	closedClients := 0
	for i, conn := range clients {
		if err := conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
			t.Logf("Failed to set read deadline for client %d: %v", i, err)
		}
		_, _, err := conn.ReadMessage()
		if err != nil {
			closedClients++
		} else {
			t.Errorf("Client %d still connected after shutdown", i)
		}
		if err := conn.Close(); err != nil {
			t.Logf("Failed to close client %d: %v", i, err)
		}
	}

	if closedClients != expectedCount {
		t.Errorf("Expected %d clients to be closed, got %d", expectedCount, closedClients)
	}
}

// TestShutdownWithActiveMessages verifies that messages in flight are delivered
// before shutdown tears the service down.
func TestShutdownWithActiveMessages(t *testing.T) {
	t.Parallel()

	svc := startService(t, ":18083")
	clients := connectTestClients(t, svc.Hub(), 2, svc.wsURL())
	sender, receiver := clients[0], clients[1]

	const messageCount = 10
	for i := range messageCount {
		if err := testhelpers.SendMessage(sender, "Test message"); err != nil {
			t.Fatalf("Failed to send message %d: %v", i, err)
		}
	}

	received := countReceivedMessages(t, receiver, messageCount)
	if received != messageCount {
		t.Errorf("Expected %d messages before shutdown, got %d", messageCount, received)
	}

	if err := svc.shutdown(t); err != nil {
		t.Errorf("Service run returned an error: %v", err)
	}
}

// TestNoClientsShutdown verifies shutdown works when no clients are connected
func TestNoClientsShutdown(t *testing.T) {
	t.Parallel()

	svc := startService(t, ":18084")

	if count := svc.Hub().ClientCount(); count != 0 {
		t.Fatalf("Expected no clients, got %d", count)
	}

	if err := svc.shutdown(t); err != nil {
		t.Errorf("Service run returned an error: %v", err)
	}
}
