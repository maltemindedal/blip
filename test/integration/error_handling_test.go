package integration

import (
	"errors"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/maltemindedal/blip/internal/server"
	"github.com/maltemindedal/blip/test/testhelpers"
)

const errMsgFailedToClose = "Failed to close connection: %v"

// errorTestServer starts a service of this test's own and returns the ws:// URL
// of its endpoint and its hub, so a test observes exactly its own clients and
// its own settings.
func errorTestServer(t *testing.T) (wsURL string, hub *server.Hub) {
	t.Helper()

	testServer, hub := newTestServer(t)
	return buildWebSocketURL(t, testServer.URL), hub
}

// TestWriteAfterCloseFails verifies that writing to a connection the client has
// already closed reports an error rather than silently succeeding.
func TestWriteAfterCloseFails(t *testing.T) {
	t.Parallel()

	wsURL, _ := errorTestServer(t)
	conn := testhelpers.Dial(t, wsURL, originOf(t, wsURL))

	if err := testhelpers.SendMessage(conn, "test"); err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	if err := conn.Close(); err != nil {
		t.Logf(errMsgFailedToClose, err)
	}

	if err := testhelpers.SendMessage(conn, "test2"); err == nil {
		t.Error("Expected an error writing to a closed connection")
	}
}

// TestReadDeadlineProducesTimeout verifies that a read deadline expiring on an
// idle connection surfaces as a timeout rather than hanging or reporting
// success.
func TestReadDeadlineProducesTimeout(t *testing.T) {
	t.Parallel()

	wsURL, _ := errorTestServer(t)
	conn := testhelpers.Dial(t, wsURL, originOf(t, wsURL))

	if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}

	_, _, err := conn.ReadMessage()
	if err == nil {
		t.Fatal("Expected a timeout error, got a successful read")
	}

	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Errorf("Expected a timeout error, got %v", err)
	}
}

// TestClientRegistersAndUnregisters verifies that the hub tracks a connection
// for exactly as long as it is open — the accounting every error path relies on.
func TestClientRegistersAndUnregisters(t *testing.T) {
	t.Parallel()

	wsURL, hub := errorTestServer(t)
	conn := testhelpers.Dial(t, wsURL, originOf(t, wsURL))

	testhelpers.WaitFor(t, 2*time.Second, "the client to register", func() bool {
		return hub.ClientCount() == 1
	})

	if err := conn.Close(); err != nil {
		t.Logf(errMsgFailedToClose, err)
	}

	testhelpers.WaitFor(t, 2*time.Second, "the client to unregister", func() bool {
		return hub.ClientCount() == 0
	})
}

// TestMalformedMessageKeepsConnectionOpen verifies that a frame the server
// cannot parse is discarded without tearing the sender's connection down: the
// next valid message from the same connection still reaches everyone else.
func TestMalformedMessageKeepsConnectionOpen(t *testing.T) {
	t.Parallel()

	wsURL, hub := errorTestServer(t)
	origin := originOf(t, wsURL)
	sender, receiver := testhelpers.DialPair(t, wsURL, origin)

	testhelpers.WaitFor(t, 2*time.Second, "both clients to register", func() bool {
		return hub.ClientCount() == 2
	})

	if err := sender.WriteMessage(websocket.TextMessage, []byte("not valid json")); err != nil {
		t.Fatalf("Failed to send malformed message: %v", err)
	}

	if err := testhelpers.SendMessage(sender, "still here"); err != nil {
		t.Fatalf("Failed to send follow-up message: %v", err)
	}

	if err := receiver.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("Failed to set read deadline: %v", err)
	}

	_, raw, err := receiver.ReadMessage()
	if err != nil {
		t.Fatalf("Sender did not survive the malformed message: %v", err)
	}

	if want := `{"content":"still here"}`; string(raw) != want {
		t.Errorf("Expected %s, got %s", want, raw)
	}
}

// originOf returns the http:// origin matching a ws:// endpoint URL.
func originOf(t *testing.T, wsURL string) string {
	t.Helper()

	parsed, err := url.Parse(wsURL)
	if err != nil {
		t.Fatalf("Failed to parse WebSocket URL: %v", err)
	}

	return "http://" + parsed.Host
}
