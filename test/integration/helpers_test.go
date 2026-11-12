package integration

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const pollInterval = 5 * time.Millisecond

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
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

func waitForServer(t *testing.T, url string, timeout time.Duration) {
	t.Helper()

	client := &http.Client{Timeout: pollInterval * 10}
	waitFor(t, timeout, "server at "+url+" to accept requests", func() bool {
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	})
}

type response struct {
	StatusCode  int
	ContentType string
	Body        string
}

func assertStatusCode(t *testing.T, resp response, expected int) {
	t.Helper()
	if resp.StatusCode != expected {
		t.Errorf("Expected status code %d, got %d", expected, resp.StatusCode)
	}
}

func assertContentType(t *testing.T, resp response, expected string) {
	t.Helper()
	if resp.ContentType != expected {
		t.Errorf("Expected content type %s, got %s", expected, resp.ContentType)
	}
}

func assertBody(t *testing.T, resp response, expected string) {
	t.Helper()
	if resp.Body != expected {
		t.Errorf("Expected body %q, got %q", expected, resp.Body)
	}
}

func makeRequest(t *testing.T, method, url string) response {
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

	return response{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Body:        string(body),
	}
}

func connectWebSocket(url string) (*websocket.Conn, error) {
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

func dialUnregistered(t *testing.T, wsURL, origin string) *websocket.Conn {
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

func sendMessage(conn *websocket.Conn, content string) error {
	message := map[string]string{"content": content}
	return conn.WriteJSON(message)
}
