package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// expectedHealthResponse is the handler's own constant, so a change to the
// served text cannot pass these tests by accident.
const expectedHealthResponse = HealthResponse

// TestHealthHandlerUnit tests the health handler function in isolation.
// It verifies that the handler responds correctly to different HTTP methods
// and returns the expected status code and response body.
func TestHealthHandlerUnit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		method         string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "GET request to health endpoint",
			method:         "GET",
			expectedStatus: http.StatusOK,
			expectedBody:   expectedHealthResponse,
		},
		{
			name:           "POST request to health endpoint",
			method:         "POST",
			expectedStatus: http.StatusOK,
			expectedBody:   expectedHealthResponse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, "/", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}

			rr := httptest.NewRecorder()

			healthHandler(rr, req)

			if status := rr.Code; status != tt.expectedStatus {
				t.Errorf("handler returned wrong status code: got %v want %v",
					status, tt.expectedStatus)
			}

			if rr.Body.String() != tt.expectedBody {
				t.Errorf("handler returned unexpected body: got %v want %v",
					rr.Body.String(), tt.expectedBody)
			}
		})
	}
}

// TestHTTPMethodsUnit tests various HTTP methods on the health endpoint.
// It verifies that the handler responds correctly to different HTTP methods
// including GET, POST, PUT, DELETE, PATCH, HEAD, and OPTIONS.
func TestHTTPMethodsUnit(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(healthHandler)

	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"}

	for _, method := range methods {
		t.Run("Test_"+method+"_method", func(t *testing.T) {
			testHTTPMethod(t, handler, method)
		})
	}
}

// testHTTPMethod tests a single HTTP method against the handler
func testHTTPMethod(t *testing.T, handler http.HandlerFunc, method string) {
	t.Helper()

	req, err := http.NewRequest(method, "/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code for %s: got %v want %v",
			method, status, http.StatusOK)
	}

	// healthHandler answers every method alike. A real server drops the body of
	// a HEAD response, so only the status is asserted for HEAD.
	if method != "HEAD" {
		expected := expectedHealthResponse
		if rr.Body.String() != expected {
			t.Errorf("handler returned unexpected body for %s: got %v want %v",
				method, rr.Body.String(), expected)
		}
	}
}

const (
	errMethodNotAllowed = "Method not allowed. WebSocket endpoint only accepts GET requests."
)

// serveWebSocket drives req through the application routes bound to a hub of
// this test's own, the way a request reaches the upgrade handler in production.
func serveWebSocket(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	w := httptest.NewRecorder()
	newRoutes(t).ServeHTTP(w, req)
	return w
}

// TestWebSocketHandlerMethodValidation tests the WebSocket handler's HTTP method validation.
// It verifies that the handler correctly rejects non-GET requests with the appropriate
// status code and error message, as WebSocket upgrades require GET requests.
func TestWebSocketHandlerMethodValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		method         string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "POST request should be rejected",
			method:         "POST",
			expectedStatus: http.StatusMethodNotAllowed,
			expectedBody:   errMethodNotAllowed,
		},
		{
			name:           "PUT request should be rejected",
			method:         "PUT",
			expectedStatus: http.StatusMethodNotAllowed,
			expectedBody:   errMethodNotAllowed,
		},
		{
			name:           "DELETE request should be rejected",
			method:         "DELETE",
			expectedStatus: http.StatusMethodNotAllowed,
			expectedBody:   errMethodNotAllowed,
		},
		{
			name:           "PATCH request should be rejected",
			method:         "PATCH",
			expectedStatus: http.StatusMethodNotAllowed,
			expectedBody:   errMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := serveWebSocket(t, httptest.NewRequest(tt.method, "/ws", nil))

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != tt.expectedStatus {
				t.Errorf("Expected status code %d, got %d", tt.expectedStatus, resp.StatusCode)
			}

			body := w.Body.String()
			if strings.TrimSpace(body) != tt.expectedBody {
				t.Errorf("Expected body %q, got %q", tt.expectedBody, strings.TrimSpace(body))
			}
		})
	}
}

// TestWebSocketHandlerGETWithoutUpgrade tests the WebSocket handler's behavior with GET requests
// that don't include proper WebSocket upgrade headers. It verifies that such requests
// are rejected with a Bad Request status code.
func TestWebSocketHandlerGETWithoutUpgrade(t *testing.T) {
	t.Parallel()

	w := serveWebSocket(t, httptest.NewRequest(http.MethodGet, "/ws", nil))

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status code %d for invalid WebSocket upgrade, got %d", http.StatusBadRequest, resp.StatusCode)
	}
}

// TestWebSocketHandlerContentType tests that the WebSocket handler sets the correct
// Content-Type header when rejecting invalid requests. It verifies that error responses
// include the appropriate content type for the error message.
func TestWebSocketHandlerContentType(t *testing.T) {
	t.Parallel()

	w := serveWebSocket(t, httptest.NewRequest(http.MethodPost, "/ws", nil))

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/plain") {
		t.Errorf("Expected Content-Type to contain 'text/plain', got %q", contentType)
	}
}

// TestNewUpgraderAppliesTheHubsSettings pins the upgrader each hub's /ws handler
// builds, as docs/reference/configuration.md documents it: 1024-byte read and
// write buffers, write buffers drawn from the pool every connection shares, and
// CheckOrigin bound to the hub's origin policy. A nil CheckOrigin would not fail
// closed: gorilla/websocket would fall back to checkSameOrigin, which accepts a
// handshake that carries no Origin header at all.
//
// It does not drive a handshake; TestHubsCarryTheirOwnOriginPolicy and the
// integration suite do.
func TestNewUpgraderAppliesTheHubsSettings(t *testing.T) {
	t.Parallel()

	policy, _ := newOriginPolicy([]string{"https://chat.example.com"})
	upgrader := newUpgrader(policy)

	if upgrader.ReadBufferSize != 1024 || upgrader.WriteBufferSize != 1024 {
		t.Errorf("buffers are %d bytes to read and %d to write, want 1024 each",
			upgrader.ReadBufferSize, upgrader.WriteBufferSize)
	}
	if upgrader.WriteBufferPool != writeBufferPool {
		t.Error("upgrader does not draw write buffers from the shared pool")
	}
	if upgrader.CheckOrigin == nil {
		t.Fatal("upgrader has no CheckOrigin, so gorilla/websocket would apply its own same-host check")
	}

	for origin, want := range map[string]bool{
		"https://chat.example.com": true,
		"https://evil.example":     false,
		"":                         false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}

		if got := upgrader.CheckOrigin(req); got != want {
			t.Errorf("CheckOrigin with Origin %q = %v, want %v", origin, got, want)
		}
	}
}

// TestWebSocketHandlerWithValidHeaders tests the WebSocket handler with valid WebSocket headers.
// It verifies that requests with proper WebSocket upgrade headers are not rejected
// with a Method Not Allowed status, ensuring the handler recognizes valid WebSocket requests.
func TestWebSocketHandlerWithValidHeaders(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)

	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "x3JJHMbDL1EzLkh9GBhXDw==")
	req.Header.Set("Origin", "http://localhost:8080")

	w := serveWebSocket(t, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusMethodNotAllowed {
		t.Error("Valid WebSocket request should not return Method Not Allowed")
	}
}

// routesAllowing builds the application routes bound to a hub of this test's
// own that allows exactly one origin.
func routesAllowing(t *testing.T, origin string) *http.ServeMux {
	t.Helper()

	cfg := NewConfig()
	cfg.AllowedOrigins = []string{origin}

	return setupRoutes(startTestHub(t, cfg))
}

// upgradeStatus drives a WebSocket handshake from origin through routes and
// reports the status. A recorder cannot be hijacked, so an accepted handshake
// still fails — later than the origin check does, and with a different status.
func upgradeStatus(t *testing.T, routes *http.ServeMux, origin string) int {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Connection", "upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "x3JJHMbDL1EzLkh9GBhXDw==")
	req.Header.Set("Origin", origin)

	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)

	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

// TestHubsCarryTheirOwnOriginPolicy pins what the configuration seam is for: the
// allow-list belongs to the hub, so two hubs in one process can disagree about
// the same origin. Each hub must accept its own and reject the other's.
func TestHubsCarryTheirOwnOriginPolicy(t *testing.T) {
	t.Parallel()

	const (
		originA = "http://a.test"
		originB = "http://b.test"
	)

	routesA := routesAllowing(t, originA)
	routesB := routesAllowing(t, originB)

	cases := []struct {
		name    string
		routes  *http.ServeMux
		origin  string
		blocked bool
	}{
		{"hub A allows its own origin", routesA, originA, false},
		{"hub A blocks hub B's origin", routesA, originB, true},
		{"hub B allows its own origin", routesB, originB, false},
		{"hub B blocks hub A's origin", routesB, originA, true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status := upgradeStatus(t, tt.routes, tt.origin)

			if blocked := status == http.StatusForbidden; blocked != tt.blocked {
				t.Errorf("Expected blocked=%v for origin %s, got status %d",
					tt.blocked, tt.origin, status)
			}
		})
	}
}
