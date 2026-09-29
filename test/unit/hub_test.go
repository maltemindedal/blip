// Package unit contains unit tests for individual components of the Blip server.
//
// These tests focus on testing specific functions and methods in isolation,
// using mocks and stubs where necessary to avoid dependencies on external systems.
// Unit tests ensure that each component behaves correctly under various conditions.
package unit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/maltemindedal/blip/internal/server"
)

const shutdownErrorMsg = "Failed to shutdown hub: %v"

// shutdownTimeout is the budget every hub shutdown in this file gets. It is
// generous because a failing shutdown should report a real error, not a race
// against a slow machine.
const shutdownTimeout = 5 * time.Second

// startHub runs a hub's event loop under cfg and shuts it down when the test
// ends. It returns once the loop is provably serving requests, so no caller
// needs to sleep before using the hub. A nil cfg gives the hub the defaults.
func startHub(t *testing.T, cfg *server.Config) *server.Hub {
	t.Helper()

	hub := server.NewHub(cfg)
	hub.Start()

	// ClientCount is answered by the hub's run loop, so a reply proves the loop
	// is up and has processed everything queued before this point.
	hub.ClientCount()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := hub.Shutdown(ctx); err != nil {
			t.Errorf(shutdownErrorMsg, err)
		}
	})

	return hub
}

// shutdownHub shuts a hub down with the standard budget and reports failures.
func shutdownHub(t *testing.T, hub *server.Hub) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	return hub.Shutdown(ctx)
}

// publishAsync calls Publish on a goroutine of its own so a caller can tell a
// rejected message from one that blocked: Publish returns on its own, but only
// a select with a deadline proves it did.
func publishAsync(hub *server.Hub, content string) <-chan bool {
	accepted := make(chan bool, 1)
	go func() {
		accepted <- hub.Publish(server.BroadcastMessage{Payload: []byte(`{"content":"` + content + `"}`)})
	}()

	return accepted
}

// TestHubStartsWithNoClients verifies that a freshly started hub reports an
// empty client set.
func TestHubStartsWithNoClients(t *testing.T) {
	t.Parallel()

	hub := startHub(t, nil)

	if count := hub.ClientCount(); count != 0 {
		t.Errorf("Expected 0 clients on a new hub, got %d", count)
	}
}

// TestHubAcceptsBroadcastWithNoClients verifies that broadcasting into an empty
// hub is accepted and leaves the hub running.
func TestHubAcceptsBroadcastWithNoClients(t *testing.T) {
	t.Parallel()

	hub := startHub(t, nil)

	select {
	case accepted := <-publishAsync(hub, "nobody home"):
		if !accepted {
			t.Fatal("Publish rejected a message on a running hub")
		}
	case <-time.After(time.Second):
		t.Fatal("Publish did not accept a message")
	}

	// The reply proves the loop finished the broadcast and came back around.
	if count := hub.ClientCount(); count != 0 {
		t.Errorf("Expected 0 clients after an empty broadcast, got %d", count)
	}
}

// TestHubHandlesConcurrentBroadcasts verifies that many goroutines can publish
// at once without deadlocking the event loop.
func TestHubHandlesConcurrentBroadcasts(t *testing.T) {
	t.Parallel()

	hub := startHub(t, nil)

	const senders = 10
	results := make([]<-chan bool, senders)
	for i := range results {
		results[i] = publishAsync(hub, "concurrent")
	}

	for _, result := range results {
		select {
		case accepted := <-result:
			if !accepted {
				t.Error("Publish rejected a message on a running hub")
			}
		case <-time.After(2 * time.Second):
			t.Error("Publish blocked under concurrent senders")
		}
	}

	if count := hub.ClientCount(); count != 0 {
		t.Errorf("Expected 0 clients after concurrent broadcasts, got %d", count)
	}
}

// TestHubShutdownStopsTheEventLoop verifies that Shutdown drains the hub and
// leaves it reporting stopped.
func TestHubShutdownStopsTheEventLoop(t *testing.T) {
	t.Parallel()

	hub := server.NewHub(nil)
	hub.Start()
	hub.ClientCount()

	if hub.IsStopped() {
		t.Fatal("Hub reported stopped while still running")
	}

	if err := shutdownHub(t, hub); err != nil {
		t.Fatalf(shutdownErrorMsg, err)
	}

	if !hub.IsStopped() {
		t.Error("Hub did not report stopped after Shutdown returned")
	}
}

// TestHubShutdownBeforeStartIsNoOp verifies that shutting down a hub that never
// ran succeeds instead of blocking on an event loop that does not exist.
func TestHubShutdownBeforeStartIsNoOp(t *testing.T) {
	t.Parallel()

	hub := server.NewHub(nil)

	if err := shutdownHub(t, hub); err != nil {
		t.Errorf("Expected shutdown of an unstarted hub to succeed, got: %v", err)
	}
}

// TestHubShutdownRightAfterStartStopsTheHub verifies that Shutdown called
// straight after Start stops the event loop, instead of returning nil because
// the loop's goroutine had not been scheduled yet and then leaving it running
// for good. The hub is shut down at once, with no barrier in between, because
// that is the case being pinned.
func TestHubShutdownRightAfterStartStopsTheHub(t *testing.T) {
	t.Parallel()

	const rounds = 200

	for range rounds {
		hub := server.NewHub(nil)
		hub.Start()

		if err := shutdownHub(t, hub); err != nil {
			t.Fatalf(shutdownErrorMsg, err)
		}

		if !hub.IsStopped() {
			t.Fatal("Hub kept running after Shutdown returned straight after Start")
		}
	}
}

// TestHubStartTwiceRunsOneLoop verifies that Start is idempotent: repeated and
// concurrent calls run a single event loop, which one Shutdown then stops. A
// second loop would close the hub's done channel twice and panic.
func TestHubStartTwiceRunsOneLoop(t *testing.T) {
	t.Parallel()

	hub := server.NewHub(nil)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(hub.Start)
	}
	wg.Wait()
	hub.Start()

	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("Expected no clients on a fresh hub, got %d", got)
	}

	if err := shutdownHub(t, hub); err != nil {
		t.Fatalf(shutdownErrorMsg, err)
	}
	if !hub.IsStopped() {
		t.Error("Hub kept running after Shutdown")
	}

	if err := shutdownHub(t, hub); err != nil {
		t.Errorf("Expected a second Shutdown to succeed, got: %v", err)
	}
}

// TestHubShutdownIsIdempotent verifies that concurrent and repeated Shutdown
// calls are safe and all report success.
func TestHubShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	hub := server.NewHub(nil)
	hub.Start()
	hub.ClientCount()

	const callers = 3
	var wg sync.WaitGroup
	wg.Add(callers)

	errs := make(chan error, callers)
	for range callers {
		go func() {
			defer wg.Done()
			errs <- shutdownHub(t, hub)
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("Concurrent shutdown returned an error: %v", err)
		}
	}

	if err := shutdownHub(t, hub); err != nil {
		t.Errorf("Shutdown after shutdown returned an error: %v", err)
	}
}

// TestHubClientCountAfterShutdown verifies that ClientCount stops blocking once
// the event loop has exited.
func TestHubClientCountAfterShutdown(t *testing.T) {
	t.Parallel()

	hub := server.NewHub(nil)
	hub.Start()
	hub.ClientCount()

	if err := shutdownHub(t, hub); err != nil {
		t.Fatalf(shutdownErrorMsg, err)
	}

	done := make(chan int, 1)
	go func() { done <- hub.ClientCount() }()

	select {
	case count := <-done:
		if count != 0 {
			t.Errorf("Expected 0 clients after shutdown, got %d", count)
		}
	case <-time.After(time.Second):
		t.Error("ClientCount blocked after the hub stopped")
	}
}

// TestHubPublishAfterShutdownIsRejected verifies that Publish loses the race
// against shutdown by reporting rejection, rather than blocking forever on an
// event loop that has stopped reading.
func TestHubPublishAfterShutdownIsRejected(t *testing.T) {
	t.Parallel()

	hub := server.NewHub(nil)
	hub.Start()
	hub.ClientCount()

	if err := shutdownHub(t, hub); err != nil {
		t.Fatalf(shutdownErrorMsg, err)
	}

	select {
	case accepted := <-publishAsync(hub, "too late"):
		if accepted {
			t.Error("Publish accepted a message on a stopped hub")
		}
	case <-time.After(time.Second):
		t.Error("Publish blocked on a stopped hub")
	}
}
