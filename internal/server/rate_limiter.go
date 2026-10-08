// Package server implements a token bucket rate limiter for per-connection
// throttling that protects the hub from abuse.
package server

import "time"

// rateLimiter is a token bucket sized for a single connection.
//
// It is deliberately lock-free: each limiter is embedded by value in a client
// and only touched by that client's read pump, so the bucket needs no
// synchronization. Do not share a limiter across goroutines.
type rateLimiter struct {
	tokens   float64
	capacity float64
	perNano  float64 // tokens replenished per nanosecond
	last     time.Time
}

// newRateLimiter builds a full bucket of capacity tokens that refills over
// interval, starting from the current instant. Capacity and interval must be
// positive: [resolveConfig] substitutes a default for anything else before a
// hub — and therefore a client — ever sees it.
func newRateLimiter(capacity int, interval time.Duration) rateLimiter {
	return newRateLimiterAt(capacity, interval, time.Now())
}

// newRateLimiterAt is [newRateLimiter] with the starting instant supplied. It is
// the constructor half of the test seam and pairs with [rateLimiter.allowAt] —
// a limiter started at one clock must be spent on that same clock, so the two
// are used together or not at all. The same test-only constraint governs both;
// it is stated once, on [rateLimiter.allowAt].
func newRateLimiterAt(capacity int, interval time.Duration, now time.Time) rateLimiter {
	perNano := float64(capacity) / float64(interval)
	if perNano <= 0 {
		perNano = float64(capacity)
	}

	return rateLimiter{
		tokens:   float64(capacity),
		capacity: float64(capacity),
		perNano:  perNano,
		last:     now,
	}
}

// allow consumes a token and reports whether the caller may proceed. It reads
// the clock itself, so a caller on this path cannot weaken the configured
// throttle by supplying an instant — there is none to supply.
//
// The zero value permits everything. A limiter only throttles once
// [newRateLimiter] has given it a capacity, so a client assembled without one
// — as tests do — is unlimited rather than silently blocked.
func (rl *rateLimiter) allow() bool {
	return rl.allowAt(time.Now())
}

// allowAt is [rateLimiter.allow] with the current instant supplied, which is
// what makes refill observable without spending wall-clock time. It exists for
// the unit tests, which advance a fixed instant by hand and pin the refill
// arithmetic exactly. A now that does not advance refills nothing, and one that
// goes backwards is ignored rather than draining the bucket.
//
// It is package-scoped, so the compiler cannot keep production code off it.
// TestClockSeamIsTestOnly is what does: outside _test.go files, allow is the
// only function permitted to name it, which is what keeps the read pump's
// throttle the configured one.
//
// The instant is a parameter rather than a field on the struct: a limiter is
// embedded by value in every client and this is a per-message path, so a
// func-valued field would cost an indirect call per message and grow every
// client.
func (rl *rateLimiter) allowAt(now time.Time) bool {
	if rl.capacity <= 0 {
		return true
	}

	if elapsed := now.Sub(rl.last); elapsed > 0 {
		rl.last = now
		rl.tokens = min(rl.capacity, rl.tokens+float64(elapsed)*rl.perNano)
	}

	if rl.tokens < 1 {
		return false
	}

	rl.tokens--
	return true
}
