// Package server normalizes and validates HTTP origins for WebSocket requests
// to enforce configured access control.
package server

import (
	"net/http"
	"net/url"
	"strings"
)

// originPolicy is the allow-list a hub checks every handshake's Origin header
// against. It is built once, by [newOriginPolicy], from the configured list and
// never mutated, so the handshake path reads it without synchronization.
type originPolicy struct {
	// origins holds every allowed origin in canonical lowercase
	// scheme://host form.
	origins map[string]struct{}

	// allowAll is set when the configured list contains "*".
	allowAll bool
}

// newOriginPolicy resolves a configured allow-list into a policy. Entries are
// trimmed and normalized to lowercase scheme://host; blank entries are skipped,
// "*" allows every origin, and anything that does not parse as an origin is
// logged and ignored rather than aborting startup.
//
// It also returns the entries it kept, normalized and in their configured
// order, with "*" kept as itself: the allow-list as enforced, for the resolved
// configuration to record. That list is nil when nothing was configured.
func newOriginPolicy(configured []string) (originPolicy, []string) {
	policy := originPolicy{origins: make(map[string]struct{}, len(configured))}

	var kept []string
	if len(configured) > 0 {
		kept = make([]string, 0, len(configured))
	}

	for _, origin := range configured {
		trimmed := strings.TrimSpace(origin)
		if trimmed == "" {
			continue
		}

		if trimmed == "*" {
			policy.allowAll = true
			kept = append(kept, trimmed)
			continue
		}

		normalized, ok := normalizeOrigin(trimmed)
		if !ok {
			log().Warn("ignoring invalid origin in configuration", "origin", origin)
			continue
		}

		policy.origins[normalized] = struct{}{}
		kept = append(kept, normalized)
	}

	return policy, kept
}

func normalizeOrigin(origin string) (string, bool) {
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", false
	}

	if parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}

	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), true
}

// allows reports whether a handshake carrying origin as its Origin header may
// open a WebSocket connection. An empty origin means the header was absent.
func (p originPolicy) allows(origin string) bool {
	// A missing Origin header is rejected even when the allow-list contains "*",
	// so non-browser clients cannot bypass the check by omitting it.
	if origin == "" {
		return false
	}

	// "*" accepts anything that carried an Origin at all, including headers
	// that do not parse as a URL, such as the literal "null" a sandboxed
	// iframe or a file:// page sends.
	if p.allowAll {
		return true
	}

	// Fast path: browsers send the already-canonical form, so the common case
	// matches the allow-list without parsing a URL.
	if _, exists := p.origins[origin]; exists {
		return true
	}

	normalized, ok := normalizeOrigin(origin)
	if !ok {
		return false
	}

	_, exists := p.origins[normalized]
	return exists
}

// checkOrigin is the policy in the shape [websocket.Upgrader] calls it, and
// logs every handshake it rejects.
func (p originPolicy) checkOrigin(r *http.Request) bool {
	if p.allows(r.Header.Get("Origin")) {
		return true
	}

	log().Warn("blocked WebSocket connection from disallowed origin", "remote_addr", r.RemoteAddr)
	return false
}
