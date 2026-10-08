package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newRoutes builds the application routes bound to a hub of this test's own, so
// no test observes another's clients. The hub takes the default configuration,
// which is all the routing tests need. It is drained when the test ends.
func newRoutes(t *testing.T) *http.ServeMux {
	t.Helper()

	return SetupRoutesWithHub(startTestHub(t, nil))
}

// TestSetupRoutes tests the route setup function.
// It verifies that SetupRoutesWithHub returns a properly configured ServeMux
// with the expected routes and handlers properly registered.
func TestSetupRoutes(t *testing.T) {
	t.Parallel()

	mux := newRoutes(t)

	// Test that the mux is not nil
	if mux == nil {
		t.Fatal("SetupRoutesWithHub returned nil mux")
	}

	// Test that the root route is properly configured
	req, err := http.NewRequest(http.MethodGet, "/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if status := rr.Code; status != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			status, http.StatusOK)
	}

	expected := expectedHealthResponse
	if rr.Body.String() != expected {
		t.Errorf("handler returned unexpected body: got %v want %v",
			rr.Body.String(), expected)
	}
}
