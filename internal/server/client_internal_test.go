package server

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestRateLimitWarnsOncePerEpisode pins that a client flooding past its burst
// costs one log line per throttling episode rather than one per discarded
// frame, that the line keeps its message and fields (operators alert on it),
// and that an allowed message starts a new episode.
//
// It swaps the process-wide logger, so it must not call t.Parallel.
func TestRateLimitWarnsOncePerEpisode(t *testing.T) {
	var logs bytes.Buffer
	SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { SetLogger(slog.New(slog.DiscardHandler)) })

	// checkRateLimit touches neither the connection nor the hub, so the client
	// can be built without them — through newClient, so its limiter and the
	// limits it logs come from one argument, as they do in production.
	const burst = 2
	c := newClient(nil, nil, "203.0.113.7:4242", defaultMaxMessageSize,
		RateLimitConfig{Burst: burst, RefillInterval: time.Hour})
	warnings := func() int { return strings.Count(logs.String(), "rate limit exceeded; discarding message") }

	for i := range burst {
		if !c.checkRateLimit() {
			t.Fatalf("message %d was denied within the burst", i)
		}
	}
	for range 50 {
		if c.checkRateLimit() {
			t.Fatal("a message was allowed past the burst")
		}
	}
	if got := warnings(); got != 1 {
		t.Fatalf("first episode logged %d lines, want 1", got)
	}
	for _, want := range []string{"level=WARN", "addr=203.0.113.7:4242", "burst=2", "interval=1h0m0s"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("warning is missing %q: %s", want, logs.String())
		}
	}

	c.rateLimiter.tokens = 1 // one refilled token ends the episode
	if !c.checkRateLimit() {
		t.Fatal("a message was denied after the bucket refilled")
	}
	for range 50 {
		c.checkRateLimit()
	}
	if got := warnings(); got != 2 {
		t.Fatalf("after a second episode logged %d lines, want 2", got)
	}
}

// TestWriteFrameCoalescesTheQueue pins the frame body clients split on: the
// message the write pump woke for, then every message already queued, each
// after a single newline, with nothing trailing and nothing left queued.
//
// It does not cover the pump around writeFrame: that a burst over a real socket
// arrives as one frame is not pinned anywhere. The integration tests split each
// frame on newlines, so they accept coalesced and separate frames alike.
func TestWriteFrameCoalescesTheQueue(t *testing.T) {
	t.Parallel()

	queued := make(chan []byte, 4)
	queued <- []byte(`{"content":"b"}`)
	queued <- []byte(`{"content":"c"}`)

	var frame bytes.Buffer
	if err := writeFrame(&frame, []byte(`{"content":"a"}`), queued); err != nil {
		t.Fatalf("writeFrame returned %v", err)
	}

	if want := "{\"content\":\"a\"}\n{\"content\":\"b\"}\n{\"content\":\"c\"}"; frame.String() != want {
		t.Errorf("frame body = %q, want %q", frame.String(), want)
	}
	if n := len(queued); n != 0 {
		t.Errorf("writeFrame left %d messages queued, want 0", n)
	}
}

// TestWriteFrameDoesNotWaitOnAnEmptyQueue pins that a frame with nothing queued
// behind it is the first message alone, written without waiting for more: the
// write pump must get back to its select, where a ping or the hub's shutdown
// can reach it.
func TestWriteFrameDoesNotWaitOnAnEmptyQueue(t *testing.T) {
	t.Parallel()

	queued := make(chan []byte, 4) // open and empty, so a receive would block

	var frame bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- writeFrame(&frame, []byte(`{"content":"a"}`), queued) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("writeFrame returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("writeFrame blocked on an empty queue")
	}

	if want := `{"content":"a"}`; frame.String() != want {
		t.Errorf("frame body = %q, want %q", frame.String(), want)
	}
}

// failingWriter accepts ok writes, then fails every write after them.
type failingWriter struct {
	ok    int
	err   error
	calls int
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls > f.ok {
		return 0, f.err
	}

	return len(p), nil
}

// TestWriteFrameStopsAtTheFirstWriteError pins that whichever write fails — the
// first message, a separator, or a queued message — its error is returned and
// nothing more is written, which is what makes the write pump stop.
func TestWriteFrameStopsAtTheFirstWriteError(t *testing.T) {
	t.Parallel()

	errBroken := errors.New("connection broken")

	// Two queued messages make five writes: a, separator, b, separator, c.
	for ok := range 5 {
		queued := make(chan []byte, 2)
		queued <- []byte(`{"content":"b"}`)
		queued <- []byte(`{"content":"c"}`)

		w := &failingWriter{ok: ok, err: errBroken}
		if err := writeFrame(w, []byte(`{"content":"a"}`), queued); !errors.Is(err, errBroken) {
			t.Errorf("failing write %d: writeFrame returned %v, want %v", ok+1, err, errBroken)
		}
		if w.calls != ok+1 {
			t.Errorf("failing write %d: writeFrame made %d writes, want %d", ok+1, w.calls, ok+1)
		}
	}
}
