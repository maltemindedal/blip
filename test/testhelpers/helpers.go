// Package testhelpers provides common utilities and helper functions for testing the Blip server.
//
// This package contains reusable test utilities for the integration tests. It provides
// functions for dialing WebSocket connections, waiting on conditions, and asserting response
// properties to reduce code duplication in test files.
package testhelpers

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// pollInterval is how often WaitFor re-evaluates its condition.
const pollInterval = 5 * time.Millisecond

// WaitFor polls cond until it reports true, failing the test if timeout elapses
// first. Use it instead of sleeping for a duration that "should be enough".
func WaitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(pollInterval)
	}

	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// WaitForServer polls url until it answers, so a test never races the listener
// that a just-started server is still opening.
func WaitForServer(t *testing.T, url string, timeout time.Duration) {
	t.Helper()

	client := &http.Client{Timeout: pollInterval * 10}
	WaitFor(t, timeout, "server at "+url+" to accept requests", func() bool {
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	})
}

// Response is a fully read HTTP response. MakeRequest closes the body before
// building one, so no test is left holding an open body.
type Response struct {
	StatusCode  int
	ContentType string
	Body        string
}

// AssertStatusCode checks if the HTTP response has the expected status code.
// It fails the test with a descriptive error message if the status codes don't match.
func AssertStatusCode(t *testing.T, resp Response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		t.Errorf("Expected status code %d, got %d", expected, resp.StatusCode)
	}
}

// AssertContentType checks if the HTTP response has the expected Content-Type header.
// It fails the test with a descriptive error message if the content types don't match.
func AssertContentType(t *testing.T, resp Response, expected string) {
	t.Helper()
	if resp.ContentType != expected {
		t.Errorf("Expected content type %s, got %s", expected, resp.ContentType)
	}
}

// AssertBody checks that the response body is exactly the expected text.
func AssertBody(t *testing.T, resp Response, expected string) {
	t.Helper()
	if resp.Body != expected {
		t.Errorf("Expected body %q, got %q", expected, resp.Body)
	}
}

// MakeRequest creates and executes an HTTP request with a 5-second timeout,
// reads the whole response, and closes the body. It fails the test if the
// request cannot be created, executed, or read.
func MakeRequest(t *testing.T, method, url string) Response {
	t.Helper()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest(method, url, http.NoBody)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	return Response{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        string(body),
	}
}

// ConnectWebSocket creates a WebSocket connection to the specified URL using the
// default development origin. It returns the connection or an error if the
// handshake fails; the caller owns closing it.
func ConnectWebSocket(url string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}

	headers := http.Header{}
	headers.Set("Origin", "http://localhost:8080")

	conn, resp, err := dialer.Dial(url, headers)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return conn, err
}

// Dial opens a WebSocket connection to wsURL presenting origin, failing the test
// if the handshake does not succeed. The connection is closed when the test ends.
func Dial(t *testing.T, wsURL, origin string) *websocket.Conn {
	t.Helper()

	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("Failed to dial %s from origin %q: %v", wsURL, origin, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// DialPair opens the sender/receiver pair that message-delivery tests need,
// both from origin. Both connections are closed when the test ends.
func DialPair(t *testing.T, wsURL, origin string) (sender, receiver *websocket.Conn) {
	t.Helper()

	return Dial(t, wsURL, origin), Dial(t, wsURL, origin)
}

// SendMessage sends a JSON message over the WebSocket connection.
// It marshals the message with a "content" field and sends it as JSON.
func SendMessage(conn *websocket.Conn, content string) error {
	message := map[string]string{"content": content}
	return conn.WriteJSON(message)
}
