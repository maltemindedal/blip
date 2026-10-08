// Package integration contains security-focused integration tests.
//
// These tests verify that the security constraints are properly enforced,
// including origin validation, message size limits, and rate limiting.
package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/maltemindedal/blip/internal/server"
)

const (
	exampleOriginHTTP = "http://example.com"

	// Error message constants
	errFailedSetReadDeadline = "Failed to set read deadline: %v"
	errExpectedTextMessage   = "Expected text message, got type %d"
	errFailedUnmarshal       = "Failed to unmarshal message: %v"
	errFailedSendMessage     = "Failed to send message %d: %v"
	errFailedReceiveMessage  = "Failed to receive message %d: %v"
)

// Helper function to assert connection should fail with forbidden status
func assertConnectionFails(t *testing.T, wsURL string, header http.Header, errorMsg string) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		_ = conn.Close()
		_ = resp.Body.Close()
		t.Fatal(errorMsg)
	}
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Expected status %d, got %d", http.StatusForbidden, resp.StatusCode)
		}
	}
}

// Helper function to assert connection succeeds
func assertConnectionSucceeds(t *testing.T, wsURL string, header http.Header, origin string) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Errorf("Expected origin %q to be allowed: %v", origin, err)
		return
	}
	_ = conn.Close()
	if resp != nil {
		_ = resp.Body.Close()
	}
}

// newOriginTestServer starts a server whose hub allows exactly origins and
// nothing else, and returns the ws:// URL of its endpoint. The allow-list
// belongs to the hub, so every case below gets a server of its own instead of
// reconfiguring a shared one.
func newOriginTestServer(t *testing.T, origins ...string) string {
	t.Helper()

	testServer, _ := newConfiguredTestServer(t, func(cfg *server.Config) {
		cfg.AllowedOrigins = origins
	})

	return testServer.wsURL()
}

// Helper function to test missing origin header
func testMissingOriginHeader(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, exampleOriginHTTP)

	header := http.Header{}
	assertConnectionFails(t, wsURL, header, "Expected connection to fail with missing origin")
}

// Helper function to test empty origin header
func testEmptyOriginHeader(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, exampleOriginHTTP)

	header := http.Header{}
	header.Set("Origin", "")
	assertConnectionFails(t, wsURL, header, "Expected connection to fail with empty origin")
}

// Helper function to test malformed origins
func testMalformedOrigins(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, exampleOriginHTTP)

	malformedOrigins := []string{
		"not-a-url",
		"://missing-scheme",
		"http://",
		"ftp://unsupported-scheme.com",
		"javascript:alert(1)",
	}

	for _, origin := range malformedOrigins {
		header := http.Header{}
		header.Set("Origin", origin)
		assertConnectionFails(t, wsURL, header,
			"Expected connection to fail with malformed origin "+origin)
	}
}

// Helper function to test case sensitivity
func testCaseSensitivity(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, exampleOriginHTTP)

	caseVariations := []string{
		"http://EXAMPLE.COM",
		"http://Example.Com",
		"HTTP://example.com",
	}

	for _, origin := range caseVariations {
		header := http.Header{}
		header.Set("Origin", origin)
		assertConnectionSucceeds(t, wsURL, header, origin)
	}
}

// Helper function to test wildcard origin. "*" accepts every origin that is
// present at all, including ones that do not parse as a URL — see
// docs/reference/configuration.md#origin-matching.
func testWildcardOrigin(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, "*")

	testOrigins := []string{
		exampleOriginHTTP,
		"https://another.com",
		"http://localhost:3000",
		// Unparseable as a URL: a sandboxed iframe or a file:// page sends
		// exactly this, and "*" must still let it through.
		"null",
		"not-a-url",
	}

	for _, origin := range testOrigins {
		header := http.Header{}
		header.Set("Origin", origin)
		assertConnectionSucceeds(t, wsURL, header, origin)
	}
}

// Helper function to test that a missing Origin is rejected even under "*".
func testWildcardStillRequiresOrigin(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, "*")

	assertConnectionFails(t, wsURL, http.Header{},
		"Expected a request with no Origin header to be rejected even under \"*\"")
}

// Helper function to test different port rejection
func testDifferentPort(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, "http://localhost:8080")

	header := http.Header{}
	header.Set("Origin", "http://localhost:9090")
	assertConnectionFails(t, wsURL, header, "Expected connection to fail with different port")
}

// Helper function to test path component handling
func testPathComponentIgnored(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, exampleOriginHTTP)

	header := http.Header{}
	header.Set("Origin", "http://example.com/some/path")
	assertConnectionSucceeds(t, wsURL, header, "http://example.com/some/path")
}

// Helper function to test HTTP vs HTTPS scheme difference
func testSchemeDifference(t *testing.T) {
	t.Helper()
	wsURL := newOriginTestServer(t, exampleOriginHTTP)

	header := http.Header{}
	header.Set("Origin", "https://example.com")
	assertConnectionFails(t, wsURL, header, "Expected HTTPS origin to be rejected when only HTTP is allowed")
}

// TestOriginValidationEdgeCases tests various edge cases for origin validation.
func TestOriginValidationEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("Missing Origin header", func(t *testing.T) {
		t.Parallel()
		testMissingOriginHeader(t)
	})

	t.Run("Empty Origin header", func(t *testing.T) {
		t.Parallel()
		testEmptyOriginHeader(t)
	})

	t.Run("Malformed Origin URL", func(t *testing.T) {
		t.Parallel()
		testMalformedOrigins(t)
	})

	t.Run("Case sensitivity in origin matching", func(t *testing.T) {
		t.Parallel()
		testCaseSensitivity(t)
	})

	t.Run("Wildcard origin configuration", func(t *testing.T) {
		t.Parallel()
		testWildcardOrigin(t)
	})

	t.Run("Wildcard still requires an Origin header", func(t *testing.T) {
		t.Parallel()
		testWildcardStillRequiresOrigin(t)
	})

	t.Run("Origin with different port", func(t *testing.T) {
		t.Parallel()
		testDifferentPort(t)
	})

	t.Run("Origin with path component ignored", func(t *testing.T) {
		t.Parallel()
		testPathComponentIgnored(t)
	})

	t.Run("HTTP vs HTTPS scheme difference", func(t *testing.T) {
		t.Parallel()
		testSchemeDifference(t)
	})
}

// newSizeLimitedServer starts a server whose hub caps messages at limit and
// returns that hub, the ws:// URL of its endpoint, and the origin to dial from.
// The limit belongs to the hub, so every case below gets a server of its own.
func newSizeLimitedServer(t *testing.T, limit int64) (hub *server.Hub, wsURL, origin string) {
	t.Helper()

	testServer, hub := newConfiguredTestServer(t, func(cfg *server.Config) {
		cfg.MaxMessageSize = limit
	})

	return hub, testServer.wsURL(), testServer.URL()
}

// Helper function to test message exactly at size limit
func testMessageAtSizeLimit(t *testing.T) {
	t.Helper()
	const limit int64 = 100

	hub, wsURL, origin := newSizeLimitedServer(t, limit)
	sender, receiver := dialPair(t, hub, wsURL, origin)

	// Create a message that's exactly at the limit.
	// JSON overhead: {"content":""} = 14 bytes, so content needs to be limit - 14
	contentSize := int(limit) - 14
	if contentSize <= 0 {
		t.Skip("Limit too small for test")
	}

	content := strings.Repeat("A", contentSize)
	payload := mustMarshalMessage(t, content)

	if int64(len(payload)) > limit {
		t.Fatalf("Payload size %d exceeds limit %d; the overhead calculation is wrong",
			len(payload), limit)
	}

	if err := sender.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("Failed to send at-limit message: %v", err)
	}

	// Receiver should get the message
	if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf(errFailedSetReadDeadline, err)
	}

	messageType, message, err := receiver.ReadMessage()
	if err != nil {
		t.Fatalf("Expected to receive at-limit message: %v", err)
	}

	if messageType != websocket.TextMessage {
		t.Errorf(errExpectedTextMessage, messageType)
	}

	var received server.Message
	if err := json.Unmarshal(message, &received); err != nil {
		t.Errorf(errFailedUnmarshal, err)
	}

	if received.Content != content {
		t.Errorf("Expected the at-limit content to arrive intact, got %d bytes", len(received.Content))
	}
}

// Helper function to test message one byte over limit
func testMessageOneByteOverLimit(t *testing.T) {
	t.Helper()
	const limit int64 = 100

	hub, wsURL, origin := newSizeLimitedServer(t, limit)
	sender, receiver := dialPair(t, hub, wsURL, origin)

	// Create message that exceeds limit by 1 byte
	oversizedContent := strings.Repeat("A", int(limit)+1)
	oversizedPayload := mustMarshalMessage(t, oversizedContent)

	if err := sender.WriteMessage(websocket.TextMessage, oversizedPayload); err != nil && !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Logf("Send error (expected): %v", err)
	}

	expectNoMessage(t, receiver, 300*time.Millisecond)
}

// Helper function to test very large message well over limit
func testVeryLargeMessage(t *testing.T) {
	t.Helper()
	const limit int64 = 64

	hub, wsURL, origin := newSizeLimitedServer(t, limit)
	sender, receiver := dialPair(t, hub, wsURL, origin)

	// Create a very large message
	hugeContent := strings.Repeat("X", int(limit)*10)
	hugePayload := mustMarshalMessage(t, hugeContent)

	if err := sender.WriteMessage(websocket.TextMessage, hugePayload); err != nil {
		t.Logf("Expected error sending huge message: %v", err)
	}

	expectNoMessage(t, receiver, 300*time.Millisecond)

	// Verify sender connection is closed
	if err := sender.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Logf("Set deadline error: %v", err)
	}
	if _, _, readErr := sender.ReadMessage(); readErr == nil {
		t.Error("Expected sender connection to be closed")
	}
}

// Helper function to test multiple small messages within limit
func testMultipleSmallMessages(t *testing.T) {
	t.Helper()
	const limit int64 = 200

	hub, wsURL, origin := newSizeLimitedServer(t, limit)
	sender, receiver := dialPair(t, hub, wsURL, origin)

	// Send multiple small messages
	for i := range 5 {
		content := strings.Repeat("A", 20)
		if err := sender.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, content)); err != nil {
			t.Errorf(errFailedSendMessage, i, err)
		}

		// Verify receiver gets it
		if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatalf(errFailedSetReadDeadline, err)
		}

		if _, _, err := receiver.ReadMessage(); err != nil {
			t.Errorf(errFailedReceiveMessage, i, err)
		}
	}
}

// Helper function to test zero-length message
func testZeroLengthMessage(t *testing.T) {
	t.Helper()
	const limit int64 = 100

	hub, wsURL, origin := newSizeLimitedServer(t, limit)
	sender, receiver := dialPair(t, hub, wsURL, origin)

	// Send message with empty content
	if err := sender.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, "")); err != nil {
		t.Errorf("Failed to send zero-length message: %v", err)
	}

	// Receiver should get it
	if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf(errFailedSetReadDeadline, err)
	}

	messageType, message, err := receiver.ReadMessage()
	if err != nil {
		t.Errorf("Failed to receive zero-length message: %v", err)
	}

	if messageType != websocket.TextMessage {
		t.Errorf(errExpectedTextMessage, messageType)
	}

	var received server.Message
	if err := json.Unmarshal(message, &received); err != nil {
		t.Errorf(errFailedUnmarshal, err)
	}

	if received.Content != "" {
		t.Errorf("Expected empty content, got %q", received.Content)
	}
}

// TestMessageSizeLimitEdgeCases tests various edge cases for message size validation.
func TestMessageSizeLimitEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("Message exactly at size limit", func(t *testing.T) {
		t.Parallel()
		testMessageAtSizeLimit(t)
	})

	t.Run("Message one byte over limit", func(t *testing.T) {
		t.Parallel()
		testMessageOneByteOverLimit(t)
	})

	t.Run("Very large message well over limit", func(t *testing.T) {
		t.Parallel()
		testVeryLargeMessage(t)
	})

	t.Run("Multiple small messages within limit", func(t *testing.T) {
		t.Parallel()
		testMultipleSmallMessages(t)
	})

	t.Run("Zero-length message", func(t *testing.T) {
		t.Parallel()
		testZeroLengthMessage(t)
	})
}

// Helper function to test invalid origin with oversized message
func testInvalidOriginWithOversizedMessage(t *testing.T) {
	t.Helper()

	testServer, _ := newConfiguredTestServer(t, func(cfg *server.Config) {
		cfg.AllowedOrigins = []string{"http://allowed.com"}
		cfg.MaxMessageSize = 64
	})
	wsURL := testServer.wsURL()

	header := http.Header{}
	header.Set("Origin", "http://blocked.com")
	assertConnectionFails(t, wsURL, header, "Expected connection to fail with invalid origin")
}

// Helper function to test valid origin with message size and rate limits
func testValidOriginWithSizeAndRateLimits(t *testing.T) {
	t.Helper()
	const burst = 3

	testServer, hub := newConfiguredTestServer(t, func(cfg *server.Config) {
		cfg.MaxMessageSize = 100
		cfg.RateLimit = server.RateLimitConfig{
			Burst:          burst,
			RefillInterval: 500 * time.Millisecond,
		}
	})

	sender, receiver := dialPair(t, hub, testServer.wsURL(), testServer.URL())

	// Send messages up to rate limit
	for i := range burst {
		if err := sender.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, "msg")); err != nil {
			t.Errorf(errFailedSendMessage, i, err)
		}

		if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatalf(errFailedSetReadDeadline, err)
		}

		if _, _, err := receiver.ReadMessage(); err != nil {
			t.Errorf(errFailedReceiveMessage, i, err)
		}
	}

	// Next message should be rate limited
	if err := sender.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, "over")); err != nil {
		t.Logf("Send error: %v", err)
	}
	expectNoMessage(t, receiver, 200*time.Millisecond)
}

// TestSecurityConstraintsCombined tests combinations of security constraints.
func TestSecurityConstraintsCombined(t *testing.T) {
	t.Parallel()

	t.Run("Invalid origin with oversized message", func(t *testing.T) {
		t.Parallel()
		testInvalidOriginWithOversizedMessage(t)
	})

	t.Run("Valid origin with message size and rate limits", func(t *testing.T) {
		t.Parallel()
		testValidOriginWithSizeAndRateLimits(t)
	})
}
