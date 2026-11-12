package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestOriginPolicyAllows pins the origin rules the security guide promises,
// against the policy alone: matching on lowercase scheme://host with the port
// included, no implied subdomains, configured entries without a scheme dropped,
// and "*" accepting any header — "null" included — but never a missing one.
//
// It does not cover how the header reaches the policy: that the upgrader calls
// checkOrigin, reads Origin from the request, and answers 403 is pinned over
// real sockets by TestWebSocketOriginValidation and
// TestOriginValidationEdgeCases in test/integration.
func TestOriginPolicyAllows(t *testing.T) {
	t.Parallel()

	const chat = "https://chat.example.com"

	tests := []struct {
		name    string
		allowed []string
		origin  string
		want    bool
	}{
		{"canonical header matches", []string{chat}, chat, true},
		{"header case is ignored", []string{chat}, "HTTPS://Chat.Example.COM", true},
		{"configured entry is trimmed and lowercased", []string{"  HTTPS://Chat.Example.com  "}, chat, true},
		{"port is part of the host", []string{"http://localhost:8080"}, "http://localhost:9090", false},
		{"host is not resolved", []string{"http://localhost:8080"}, "http://127.0.0.1:8080", false},
		{"scheme is part of the origin", []string{chat}, "http://chat.example.com", false},
		{"subdomains are not implied", []string{"https://example.com"}, chat, false},
		{"entry without a scheme is dropped", []string{"example.com"}, "https://example.com", false},
		{"dropped entry does not match itself", []string{"example.com"}, "example.com", false},
		{"invalid entry leaves valid ones working", []string{"example.com", chat}, chat, true},
		{"star allows any origin", []string{"*"}, "https://anywhere.example", true},
		{"star allows the null origin", []string{"*"}, "null", true},
		{"star is trimmed", []string{chat, " * "}, "https://anywhere.example", true},
		{"missing header is rejected under star", []string{"*"}, "", false},
		{"missing header is rejected", []string{chat}, "", false},
		{"null origin is rejected without star", []string{chat}, "null", false},
		{"empty allow-list allows nothing", nil, chat, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := newOriginPolicy(tt.allowed)
			if got := policy.allows(tt.origin); got != tt.want {
				t.Errorf("newOriginPolicy(%q).allows(%q) = %v, want %v", tt.allowed, tt.origin, got, tt.want)
			}
		})
	}
}

// BenchmarkOriginCheck measures the check the upgrader runs on every handshake,
// with the already-canonical header a browser sends.
func BenchmarkOriginCheck(b *testing.B) {
	policy := newOriginPolicy([]string{"http://localhost:8080", "https://example.com"})

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Origin", "https://example.com")

	b.ReportAllocs()
	for b.Loop() {
		if !policy.checkOrigin(req) {
			b.Fatal("expected origin to be allowed")
		}
	}
}

// TestOriginCheckDoesNotAllocate pins the handshake's origin check at zero
// allocations for the already-canonical header a browser sends. It cannot run
// in parallel: AllocsPerRun counts every allocation in the process and panics
// inside a parallel test.
//
// It covers an allowed, canonical origin. A header that needs normalizing parses
// a URL, and a rejected one logs; both may allocate.
func TestOriginCheckDoesNotAllocate(t *testing.T) {
	policy := newOriginPolicy([]string{"http://localhost:8080", "https://example.com"})
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Origin", "https://example.com")

	allocs := testing.AllocsPerRun(1000, func() {
		if !policy.checkOrigin(req) {
			t.Fatal("expected origin to be allowed")
		}
	})
	if allocs != 0 {
		t.Errorf("checkOrigin allocates %v times per handshake, want 0", allocs)
	}
}
