package server

import (
	"bytes"
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

	const burst = 2
	c := &Client{
		addr:        "203.0.113.7:4242",
		rateLimit:   RateLimitConfig{Burst: burst, RefillInterval: time.Hour},
		rateLimiter: newRateLimiter(burst, time.Hour),
	}
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
