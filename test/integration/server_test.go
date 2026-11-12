package integration

import (
	"net/http"
	"testing"

	"github.com/maltemindedal/blip/internal/server"
)

// TestHealthEndpointIntegration tests the health endpoint with the actual server configuration.
// It verifies that the complete server setup including routing, handlers, and HTTP responses
// work correctly together in a real server environment.
func TestHealthEndpointIntegration(t *testing.T) {
	t.Parallel()

	testServer, _ := newTestServer(t)

	resp := makeRequest(t, http.MethodGet, testServer.URL()+"/")

	assertStatusCode(t, resp, http.StatusOK)
	assertContentType(t, resp, "text/plain")
	assertBody(t, resp, server.HealthResponse)
}

// TestUnmatchedPathsServeHealth verifies the documented catch-all behavior: `/`
// is registered as the ServeMux fallback, so every unmatched path returns the
// health response rather than a 404.
func TestUnmatchedPathsServeHealth(t *testing.T) {
	t.Parallel()

	testServer, _ := newTestServer(t)

	resp := makeRequest(t, http.MethodGet, testServer.URL()+"/nonexistent")

	assertStatusCode(t, resp, http.StatusOK)
	assertBody(t, resp, server.HealthResponse)
}

// TestFullServerIntegration tests the complete startup path: the real service,
// on a real port, serving over its own listener. It verifies that configuration,
// routing, handlers, and the HTTP server work together when assembled by
// [server.New] and driven by Run, the way main does it.
//
// The HTTP settings that service applies are pinned in
// internal/server/service_internal_test.go, where they are reachable.
func TestFullServerIntegration(t *testing.T) {
	t.Parallel()

	svc := startService(t, ":18086")

	resp := makeRequest(t, http.MethodGet, svc.URL()+"/")
	assertStatusCode(t, resp, http.StatusOK)
	assertContentType(t, resp, "text/plain")
	assertBody(t, resp, server.HealthResponse)

	if err := svc.shutdown(t); err != nil {
		t.Errorf("Service run returned an error: %v", err)
	}
}
