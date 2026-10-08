package server

import (
	"context"
	"log/slog"
	"os"
	"strconv"
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

// startTestHub runs a hub's event loop and shuts it down when the test ends. It
// returns once the loop is provably serving requests, so no caller has to sleep
// before using the hub.
func startTestHub(t *testing.T) *Hub {
	t.Helper()

	h := NewHub(nil)
	h.Start()

	// ClientCount is answered by the run loop, so a reply proves it is up.
	h.ClientCount()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("failed to shut the hub down: %v", err)
		}
	})

	return h
}

// registerFake registers a fake client with an inbox buffer slots deep.
func registerFake(t *testing.T, h *Hub, addr string, buffer int) *fakeClient {
	t.Helper()

	c := newFakeClient(addr, buffer)
	if !h.Register(t.Context(), c) {
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

	h := NewHub(nil)
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
			msg := BroadcastMessage{
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

	h := startTestHub(t)
	slow := registerFake(t, h, "slow", 1)
	fast := registerFake(t, h, "fast", sendBufferSz)

	for _, content := range []string{"one", "two"} {
		if !h.Publish(BroadcastMessage{Payload: []byte(`{"content":"` + content + `"}`)}) {
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
// message, which is the reason BroadcastMessage carries a sender at all.
func TestHubBroadcastSkipsTheSender(t *testing.T) {
	t.Parallel()

	h := startTestHub(t)
	sender := registerFake(t, h, "sender", sendBufferSz)
	other := registerFake(t, h, "other", sendBufferSz)

	if !h.Publish(BroadcastMessage{Sender: sender, Payload: []byte(`{"content":"hi"}`)}) {
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

	h := startTestHub(t)

	clients := make([]*fakeClient, 6)
	for i := range clients {
		clients[i] = registerFake(t, h, "client-"+strconv.Itoa(i), sendBufferSz)
	}

	payload := []byte(`{"content":"everyone"}`)
	if !h.Publish(BroadcastMessage{Sender: clients[0], Payload: payload}) {
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

	h := startTestHub(t)
	stays := registerFake(t, h, "stays", sendBufferSz)
	leaves := registerFake(t, h, "leaves", sendBufferSz)
	stranger := newFakeClient("stranger", sendBufferSz)

	h.Unregister(stranger)
	h.Unregister(leaves)
	h.Unregister(leaves)

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

// TestHubRejectsClientWorkAfterShutdown pins the shutdown race that Register and
// Unregister now own: once the run loop has exited, neither may block on a
// channel it will never read again. Both take a clientConn, which only this
// package can name, so the test lives here.
func TestHubRejectsClientWorkAfterShutdown(t *testing.T) {
	t.Parallel()

	h := NewHub(nil)
	h.Start()
	h.ClientCount()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if err := h.Shutdown(ctx); err != nil {
		t.Fatalf("failed to shut the hub down: %v", err)
	}

	client := newFakeClient("shutdown-race", 0)

	registered := make(chan bool, 1)
	go func() { registered <- h.Register(t.Context(), client) }()

	select {
	case accepted := <-registered:
		if accepted {
			t.Error("Register accepted a client on a stopped hub")
		}
	case <-time.After(time.Second):
		t.Fatal("Register blocked on a stopped hub")
	}

	unregistered := make(chan struct{})
	go func() {
		h.Unregister(client)
		close(unregistered)
	}()

	select {
	case <-unregistered:
	case <-time.After(time.Second):
		t.Fatal("Unregister blocked on a stopped hub")
	}
}
