package server

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestMain silences server logging so benchmark output stays readable.
func TestMain(m *testing.M) {
	SetLogger(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// fakeClient is the test-side [clientConn]: an inbox and an address, with no
// socket underneath. It is what makes the hub's delivery rules reachable — a
// client whose buffer fills faster than it drains cannot be arranged on demand
// over a real connection.
type fakeClient struct {
	send chan []byte
	addr string
}

func newFakeClient(addr string, buffer int) *fakeClient {
	return &fakeClient{send: make(chan []byte, buffer), addr: addr}
}

func (f *fakeClient) inbox() chan<- []byte { return f.send }
func (f *fakeClient) remoteAddr() string   { return f.addr }

// serve is the goroutine the hub runs per client. A fake has no pumps, so it
// returns immediately and the hub's WaitGroup drops straight back to zero.
func (f *fakeClient) serve() {}

// closeConnection is what the hub calls on every client at shutdown. There is
// no connection to close.
func (f *fakeClient) closeConnection() {}

// drain reads everything queued on the fake's inbox and reports whether the hub
// closed it. Every read is non-blocking, so a test that catches a regression
// fails on the assertion rather than hanging on a channel nobody will feed.
func drain(f *fakeClient) (got [][]byte, closed bool) {
	for {
		select {
		case msg, ok := <-f.send:
			if !ok {
				return got, true
			}
			got = append(got, msg)
		default:
			return got, false
		}
	}
}

// hubShutdownBudget is the deadline every hub shutdown in these tests gets
// unless it is deliberately testing a short one. It is generous because a
// failing shutdown should report a real error, not a race against a slow
// machine.
const hubShutdownBudget = 5 * time.Second

// startTestHub runs a hub's event loop under cfg and shuts it down when the test
// ends. It returns once the loop is provably serving requests, so no caller has
// to sleep before using the hub. A nil cfg gives the hub the defaults.
func startTestHub(t *testing.T, cfg *Config) *Hub {
	t.Helper()

	h := newHub(cfg)
	h.start()

	// ClientCount is answered by the run loop, so a reply proves it is up and has
	// processed everything queued before this point.
	h.ClientCount()

	shutdownAtCleanup(t, h)
	return h
}

// shutdownAtCleanup shuts h down when the test ends, so a test that fails before
// its own shutdown does not leave the run loop running for the rest of the test
// binary. shutdown is idempotent, so a test that has already shut h down loses
// nothing.
func shutdownAtCleanup(t *testing.T, h *Hub) {
	t.Helper()

	t.Cleanup(func() {
		if err := shutdownHub(t, h); err != nil {
			t.Errorf(shutdownErrorMsg, err)
		}
	})
}

const shutdownErrorMsg = "Failed to shutdown hub: %v"

// shutdownHub shuts a hub down within hubShutdownBudget.
func shutdownHub(t *testing.T, h *Hub) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), hubShutdownBudget)
	defer cancel()

	return h.shutdown(ctx)
}

// registerFake registers a fake client with an inbox buffer slots deep.
func registerFake(t *testing.T, h *Hub, addr string, buffer int) *fakeClient {
	t.Helper()

	c := newFakeClient(addr, buffer)
	if !h.register(t.Context(), c) {
		t.Fatalf("hub refused to register %s", addr)
	}

	return c
}

// newBenchHub builds a hub holding n clients installed through the hub's own
// registration path, so the benchmark measures the fan-out over a client set
// the hub assembled itself. The run loop is deliberately left unstarted: the
// benchmark calls handleBroadcast directly, which keeps the client map owned by
// the one goroutine touching it and keeps pump scheduling out of the number.
func newBenchHub(tb testing.TB, n int) (*Hub, []*fakeClient) {
	tb.Helper()

	h := newHub(nil)
	clients := make([]*fakeClient, n)

	for i := range clients {
		clients[i] = newFakeClient("bench-"+strconv.Itoa(i), sendBufferSz)
		h.addClient(clients[i])
	}

	return h, clients
}

func BenchmarkHubBroadcast(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(strconv.Itoa(n)+"clients", func(b *testing.B) {
			h, clients := newBenchHub(b, n)
			msg := broadcastMessage{
				Sender:  clients[0],
				Payload: []byte(`{"content":"hello everyone"}`),
			}

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				h.handleBroadcast(msg)

				// Drain inline. A benchmark loop outruns any consumer
				// goroutine, and the hub evicts clients whose buffers fill,
				// which would shrink the fan-out mid-measurement.
				for _, c := range clients[1:] {
					<-c.send
				}
			}

			if len(h.clients) != n {
				b.Fatalf("hub dropped clients during the benchmark: %d of %d remain", len(h.clients), n)
			}
		})
	}
}

// TestHubDropsClientWithAFullInbox pins the backpressure rule: fan-out is a
// non-blocking send, and a client that cannot take a message is dropped from
// the registry rather than allowed to stall the broadcast for everyone else.
// A one-slot inbox is not the real buffer size — what happens once it is full
// is the behavior, and filling 256 slots would only make the test slower.
func TestHubDropsClientWithAFullInbox(t *testing.T) {
	t.Parallel()

	h := startTestHub(t, nil)
	slow := registerFake(t, h, "slow", 1)
	fast := registerFake(t, h, "fast", sendBufferSz)

	for _, content := range []string{"one", "two"} {
		if !h.publish(broadcastMessage{Payload: []byte(`{"content":"` + content + `"}`)}) {
			t.Fatalf("hub refused the %q broadcast", content)
		}
	}

	// The reply proves both fan-outs have been processed.
	if count := h.ClientCount(); count != 1 {
		t.Errorf("expected the full client to be dropped, leaving 1, got %d", count)
	}

	// The fast client got both, so the full one never stalled the fan-out.
	if got, _ := drain(fast); len(got) != 2 {
		t.Errorf("expected the fast client to receive 2 messages, got %d", len(got))
	}

	got, closed := drain(slow)
	if len(got) != 1 {
		t.Errorf("expected the dropped client to hold the 1 message it took, got %d", len(got))
	}

	// Dropping closes the inbox, which is what stops the client's write pump.
	if !closed {
		t.Error("hub dropped the client without closing its inbox")
	}
}

// TestHubBroadcastSkipsTheSender pins that a client never receives its own
// message, which is the reason broadcastMessage carries a sender at all.
func TestHubBroadcastSkipsTheSender(t *testing.T) {
	t.Parallel()

	h := startTestHub(t, nil)
	sender := registerFake(t, h, "sender", sendBufferSz)
	other := registerFake(t, h, "other", sendBufferSz)

	if !h.publish(broadcastMessage{Sender: sender, Payload: []byte(`{"content":"hi"}`)}) {
		t.Fatal("hub refused the broadcast")
	}
	h.ClientCount()

	if got, _ := drain(sender); len(got) != 0 {
		t.Errorf("sender received its own message: %q", got)
	}
	if got, _ := drain(other); len(got) != 1 {
		t.Errorf("expected the other client to receive 1 message, got %d", len(got))
	}
}

// TestHubBroadcastReachesEveryOtherClient pins the fan-out itself: every
// registered client except the sender gets the payload, unchanged.
func TestHubBroadcastReachesEveryOtherClient(t *testing.T) {
	t.Parallel()

	h := startTestHub(t, nil)

	clients := make([]*fakeClient, 6)
	for i := range clients {
		clients[i] = registerFake(t, h, "client-"+strconv.Itoa(i), sendBufferSz)
	}

	payload := []byte(`{"content":"everyone"}`)
	if !h.publish(broadcastMessage{Sender: clients[0], Payload: payload}) {
		t.Fatal("hub refused the broadcast")
	}

	if count := h.ClientCount(); count != len(clients) {
		t.Fatalf("expected %d clients after the broadcast, got %d", len(clients), count)
	}

	for _, c := range clients[1:] {
		got, _ := drain(c)
		if len(got) != 1 {
			t.Errorf("%s received %d messages, want 1", c.addr, len(got))
			continue
		}

		if string(got[0]) != string(payload) {
			t.Errorf("%s received %q, want %q", c.addr, got[0], payload)
		}
	}
}

// TestHubUnregisterOfAGoneClientIsANoOp pins that removing a client the hub does
// not hold changes nothing: no panic from closing an inbox twice, and no other
// client disturbed. A client that was already dropped for backpressure and one
// that never registered both arrive here.
func TestHubUnregisterOfAGoneClientIsANoOp(t *testing.T) {
	t.Parallel()

	h := startTestHub(t, nil)
	stays := registerFake(t, h, "stays", sendBufferSz)
	leaves := registerFake(t, h, "leaves", sendBufferSz)
	stranger := newFakeClient("stranger", sendBufferSz)

	h.unregister(stranger)
	h.unregister(leaves)
	h.unregister(leaves)

	if count := h.ClientCount(); count != 1 {
		t.Fatalf("expected 1 client to remain, got %d", count)
	}

	if _, closed := drain(leaves); !closed {
		t.Error("unregistering a client left its inbox open")
	}
	if _, closed := drain(stranger); closed {
		t.Error("hub closed the inbox of a client it never held")
	}

	if got, closed := drain(stays); len(got) != 0 || closed {
		t.Errorf("the remaining client received %d messages, inbox closed: %t", len(got), closed)
	}
}

// TestHubRejectsClientWorkAfterShutdown pins the shutdown race that register and
// unregister own: once the run loop has exited, neither may block on a channel
// it will never read again.
func TestHubRejectsClientWorkAfterShutdown(t *testing.T) {
	t.Parallel()

	h := startTestHub(t, nil)

	if err := shutdownHub(t, h); err != nil {
		t.Fatalf(shutdownErrorMsg, err)
	}

	client := newFakeClient("shutdown-race", 0)

	registered := make(chan bool, 1)
	go func() { registered <- h.register(t.Context(), client) }()

	select {
	case accepted := <-registered:
		if accepted {
			t.Error("register accepted a client on a stopped hub")
		}
	case <-time.After(time.Second):
		t.Fatal("register blocked on a stopped hub")
	}

	unregistered := make(chan struct{})
	go func() {
		h.unregister(client)
		close(unregistered)
	}()

	select {
	case <-unregistered:
	case <-time.After(time.Second):
		t.Fatal("unregister blocked on a stopped hub")
	}
}

// publishAsync calls publish on a goroutine of its own so a caller can tell a
// rejected message from one that blocked: publish returns on its own, but only
// a select with a deadline proves it did.
func publishAsync(hub *Hub, content string) <-chan bool {
	accepted := make(chan bool, 1)
	go func() {
		accepted <- hub.publish(broadcastMessage{Payload: []byte(`{"content":"` + content + `"}`)})
	}()

	return accepted
}

// TestHubStartsWithNoClients verifies that a freshly started hub reports an
// empty client set.
func TestHubStartsWithNoClients(t *testing.T) {
	t.Parallel()

	hub := startTestHub(t, nil)

	if count := hub.ClientCount(); count != 0 {
		t.Errorf("Expected 0 clients on a new hub, got %d", count)
	}
}

// TestHubAcceptsBroadcastWithNoClients verifies that broadcasting into an empty
// hub is accepted and leaves the hub running.
func TestHubAcceptsBroadcastWithNoClients(t *testing.T) {
	t.Parallel()

	hub := startTestHub(t, nil)

	select {
	case accepted := <-publishAsync(hub, "nobody home"):
		if !accepted {
			t.Fatal("publish rejected a message on a running hub")
		}
	case <-time.After(time.Second):
		t.Fatal("publish did not accept a message")
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

	hub := startTestHub(t, nil)

	const senders = 10
	results := make([]<-chan bool, senders)
	for i := range results {
		results[i] = publishAsync(hub, "concurrent")
	}

	for _, result := range results {
		select {
		case accepted := <-result:
			if !accepted {
				t.Error("publish rejected a message on a running hub")
			}
		case <-time.After(2 * time.Second):
			t.Error("publish blocked under concurrent senders")
		}
	}

	if count := hub.ClientCount(); count != 0 {
		t.Errorf("Expected 0 clients after concurrent broadcasts, got %d", count)
	}
}

// TestHubShutdownStopsTheEventLoop verifies that shutdown drains the hub and
// leaves it reporting stopped.
func TestHubShutdownStopsTheEventLoop(t *testing.T) {
	t.Parallel()

	hub := startTestHub(t, nil)

	if hub.IsStopped() {
		t.Fatal("Hub reported stopped while still running")
	}

	if err := shutdownHub(t, hub); err != nil {
		t.Fatalf(shutdownErrorMsg, err)
	}

	if !hub.IsStopped() {
		t.Error("Hub did not report stopped after shutdown returned")
	}
}

// TestHubShutdownBeforeStartIsNoOp verifies that shutting down a hub that never
// ran succeeds instead of blocking on an event loop that does not exist.
func TestHubShutdownBeforeStartIsNoOp(t *testing.T) {
	t.Parallel()

	hub := newHub(nil)

	if err := shutdownHub(t, hub); err != nil {
		t.Errorf("Expected shutdown of an unstarted hub to succeed, got: %v", err)
	}
}

// TestHubShutdownRightAfterStartStopsTheHub verifies that shutdown called
// straight after start stops the event loop, instead of returning nil because
// the loop's goroutine had not been scheduled yet and then leaving it running
// for good. The hub is shut down at once, with no barrier in between, because
// that is the case being pinned.
func TestHubShutdownRightAfterStartStopsTheHub(t *testing.T) {
	t.Parallel()

	const rounds = 200

	for range rounds {
		hub := newHub(nil)
		shutdownAtCleanup(t, hub)
		hub.start()

		if err := shutdownHub(t, hub); err != nil {
			t.Fatalf(shutdownErrorMsg, err)
		}

		if !hub.IsStopped() {
			t.Fatal("Hub kept running after shutdown returned straight after start")
		}
	}
}

// TestHubStartTwiceRunsOneLoop verifies that start is idempotent: repeated and
// concurrent calls run a single event loop, which one shutdown then stops. A
// second loop would close the hub's done channel twice and panic.
func TestHubStartTwiceRunsOneLoop(t *testing.T) {
	t.Parallel()

	hub := newHub(nil)
	shutdownAtCleanup(t, hub)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(hub.start)
	}
	wg.Wait()
	hub.start()

	if got := hub.ClientCount(); got != 0 {
		t.Fatalf("Expected no clients on a fresh hub, got %d", got)
	}

	if err := shutdownHub(t, hub); err != nil {
		t.Fatalf(shutdownErrorMsg, err)
	}
	if !hub.IsStopped() {
		t.Error("Hub kept running after shutdown")
	}

	if err := shutdownHub(t, hub); err != nil {
		t.Errorf("Expected a second shutdown to succeed, got: %v", err)
	}
}

// TestHubShutdownIsIdempotent verifies that concurrent and repeated shutdown
// calls are safe and all report success.
func TestHubShutdownIsIdempotent(t *testing.T) {
	t.Parallel()

	hub := startTestHub(t, nil)

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
		t.Errorf("shutdown after shutdown returned an error: %v", err)
	}
}

// TestHubClientCountAfterShutdown verifies that ClientCount stops blocking once
// the event loop has exited.
func TestHubClientCountAfterShutdown(t *testing.T) {
	t.Parallel()

	hub := startTestHub(t, nil)

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

// TestHubPublishAfterShutdownIsRejected verifies that publish loses the race
// against shutdown by reporting rejection, rather than blocking forever on an
// event loop that has stopped reading.
func TestHubPublishAfterShutdownIsRejected(t *testing.T) {
	t.Parallel()

	hub := startTestHub(t, nil)

	if err := shutdownHub(t, hub); err != nil {
		t.Fatalf(shutdownErrorMsg, err)
	}

	select {
	case accepted := <-publishAsync(hub, "too late"):
		if accepted {
			t.Error("publish accepted a message on a stopped hub")
		}
	case <-time.After(time.Second):
		t.Error("publish blocked on a stopped hub")
	}
}

// TestHubShutdownReturnsPromptlyWhenIdle verifies that shutdown returns promptly
// rather than blocking for its whole budget when there is nothing left to
// drain.
func TestHubShutdownReturnsPromptlyWhenIdle(t *testing.T) {
	t.Parallel()

	hub := startTestHub(t, nil)

	// A budget this short is only met if shutdown returns as soon as the event
	// loop and the pumps are done, rather than waiting out a timer.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := hub.shutdown(ctx); err != nil {
		t.Errorf("Expected an idle hub to shut down within its budget, got: %v", err)
	}
}
