package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/maltemindedal/blip/internal/server"
)

// TestWebSocketEndpointIntegration tests the WebSocket endpoint with full server integration.
// It verifies that WebSocket connections can be established, messages can be sent and received,
// and the complete WebSocket functionality works in a real server environment.
func TestWebSocketEndpointIntegration(t *testing.T) {
	t.Parallel()

	testServer, _ := newTestServer(t)

	wsURL := testServer.wsURL()

	t.Run("Successful WebSocket Connection", func(t *testing.T) {
		testSuccessfulWebSocketConnection(t, wsURL, testServer.URL)
	})

	t.Run("Invalid HTTP Method", func(t *testing.T) {
		testInvalidHTTPMethod(t, testServer.URL)
	})

	t.Run("GET Without WebSocket Headers", func(t *testing.T) {
		testGETWithoutWebSocketHeaders(t, testServer.URL)
	})
}

// testSuccessfulWebSocketConnection tests establishing a WebSocket connection and sending messages
func testSuccessfulWebSocketConnection(t *testing.T, wsURL, serverURL string) {
	t.Helper()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, newOriginHeader(serverURL))
	if err != nil {
		t.Fatalf("Failed to connect to WebSocket: %v", err)
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("Expected status %d, got %d", http.StatusSwitchingProtocols, resp.StatusCode)
	}

	testMessage := "Hello, WebSocket!"
	err = conn.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, testMessage))
	if err != nil {
		t.Errorf("Failed to send message: %v", err)
	}

	err = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	if err != nil {
		t.Errorf("Failed to send close message: %v", err)
	}
}

// testInvalidHTTPMethod verifies that POST requests to WebSocket endpoint are rejected
func testInvalidHTTPMethod(t *testing.T, serverURL string) {
	t.Helper()

	resp, err := http.Post(serverURL+"/ws", "text/plain", strings.NewReader("test"))
	if err != nil {
		t.Fatalf("Failed to make POST request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("Expected status %d for POST request, got %d", http.StatusMethodNotAllowed, resp.StatusCode)
	}
}

// testGETWithoutWebSocketHeaders verifies that GET requests without WebSocket headers are rejected
func testGETWithoutWebSocketHeaders(t *testing.T, serverURL string) {
	t.Helper()

	resp, err := http.Get(serverURL + "/ws")
	if err != nil {
		t.Fatalf("Failed to make GET request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status %d for GET without WebSocket headers, got %d", http.StatusBadRequest, resp.StatusCode)
	}
}

// TestWebSocketMessageBroadcasting tests the WebSocket message broadcasting functionality.
// It verifies that messages sent by one client are properly broadcasted to all other
// connected clients through the hub system.
func TestWebSocketMessageBroadcasting(t *testing.T) {
	t.Parallel()

	testServer, hub := newTestServer(t)

	wsURL := testServer.wsURL()
	connections := dialClients(t, hub, wsURL, testServer.URL, 3)

	messageContent := "Hello from client 0!"
	sendMessageFromClient(t, connections[0], messageContent)
	verifyMessageReceivedByOtherClients(t, connections, messageContent, 0)
	expectNoMessage(t, connections[0], 200*time.Millisecond)

	testMalformedMessageIgnored(t, connections)
	closeAllConnections(t, connections)
}

// sendMessageFromClient sends a message from a specific client
func sendMessageFromClient(t *testing.T, conn *websocket.Conn, content string) {
	t.Helper()

	if err := conn.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, content)); err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}
}

// verifyMessageReceivedByOtherClients checks that all clients except sender receive the message
func verifyMessageReceivedByOtherClients(t *testing.T, connections []*websocket.Conn, expectedContent string, senderIndex int) {
	t.Helper()

	for i := range connections {
		if i == senderIndex {
			continue
		}
		verifyClientReceivesMessage(t, connections[i], expectedContent, i)
	}
}

// verifyClientReceivesMessage verifies a single client receives the expected message.
// Handles batched messages separated by newlines.
func verifyClientReceivesMessage(t *testing.T, conn *websocket.Conn, expectedContent string, clientIndex int) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Errorf("Failed to set read deadline for client %d: %v", clientIndex, err)
		return
	}

	messageType, message, err := conn.ReadMessage()
	if err != nil {
		t.Errorf("Client %d failed to receive broadcasted message: %v", clientIndex, err)
		return
	}

	if messageType != websocket.TextMessage {
		t.Errorf("Client %d: Expected text message, got type %d", clientIndex, messageType)
		return
	}

	// Handle batched messages - split by newline and check each part
	parts := bytes.Split(message, []byte("\n"))
	found := false

	for _, part := range parts {
		if len(part) == 0 {
			continue
		}

		var received server.Message
		if err := json.Unmarshal(part, &received); err != nil {
			t.Errorf("Client %d: Failed to unmarshal message part: %v", clientIndex, err)
			continue
		}

		if received.Content == expectedContent {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("Client %d: Expected content %q not found in received message(s)", clientIndex, expectedContent)
	}
}

// testMalformedMessageIgnored sends malformed JSON and verifies it's ignored by all clients
func testMalformedMessageIgnored(t *testing.T, connections []*websocket.Conn) {
	t.Helper()

	if err := connections[1].WriteMessage(websocket.TextMessage, []byte("not valid json")); err != nil {
		t.Fatalf("Failed to send malformed message: %v", err)
	}

	for i := range connections {
		if i == 1 {
			continue
		}
		expectNoMessage(t, connections[i], 150*time.Millisecond)
	}
}

// closeAllConnections gracefully closes all WebSocket connections
func closeAllConnections(t *testing.T, connections []*websocket.Conn) {
	t.Helper()

	for i, conn := range connections {
		err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		if err != nil {
			t.Errorf("Failed to send close message for client %d: %v", i, err)
		}
	}
}

// TestWebSocketConnectionLifecycle tests the complete lifecycle of WebSocket connections.
// It verifies that connections can be established, used for communication, and properly
// closed, including testing multiple sequential connections.
func TestWebSocketConnectionLifecycle(t *testing.T) {
	t.Parallel()

	testServer, hub := newTestServer(t)
	wsURL := testServer.wsURL()

	t.Run("Connection and Disconnection", func(t *testing.T) {
		conn := dial(t, hub, wsURL, testServer.URL)

		// Test that connection is active
		if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			t.Errorf("Failed to send ping: %v", err)
		}

		if err := conn.Close(); err != nil {
			t.Errorf("Failed to close connection: %v", err)
		}

		waitForUnregister(t, hub, 0)
	})

	t.Run("Multiple Sequential Connections", func(t *testing.T) {
		// Each connection must be fully registered and then fully unregistered
		// before the next one, which is what makes the count assertions exact.
		for i := range 3 {
			conn := dial(t, hub, wsURL, testServer.URL)

			testMsg := "Test message " + strconv.Itoa(i)
			if err := conn.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, testMsg)); err != nil {
				t.Errorf("Failed to send message on iteration %d: %v", i, err)
			}

			if err := conn.Close(); err != nil {
				t.Errorf("Failed to close connection on iteration %d: %v", i, err)
			}

			waitForUnregister(t, hub, 0)
		}
	})
}

// TestWebSocketConcurrentConnections tests concurrent WebSocket connections.
// It verifies that multiple clients can connect simultaneously and exchange messages
// without causing race conditions or system instability.
func TestWebSocketConcurrentConnections(t *testing.T) {
	t.Parallel()

	testServer, _ := newTestServer(t)

	wsURL := testServer.wsURL()

	const numConcurrentClients = 10
	done := make(chan error, numConcurrentClients)

	launchConcurrentClients(wsURL, testServer.URL, numConcurrentClients, done)
	waitForConcurrentClients(t, numConcurrentClients, done)
}

// launchConcurrentClients starts multiple WebSocket clients concurrently
func launchConcurrentClients(wsURL, serverURL string, numClients int, done chan error) {
	for i := range numClients {
		message := "Message from client " + strconv.Itoa(i)
		payload, err := json.Marshal(server.Message{Content: message})
		if err != nil {
			done <- fmt.Errorf("failed to marshal message for client %d: %w", i, err)
			continue
		}

		go runConcurrentClient(i, wsURL, serverURL, payload, done)
	}
}

// runConcurrentClient runs a single concurrent WebSocket client
func runConcurrentClient(clientID int, wsURL, serverURL string, msgPayload []byte, done chan error) {
	defer func() {
		if r := recover(); r != nil {
			done <- fmt.Errorf("client %d panic: %v", clientID, r)
		}
	}()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, newOriginHeader(serverURL))
	if err != nil {
		done <- fmt.Errorf("client %d dial: %w", clientID, err)
		return
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()

	if err := conn.WriteMessage(websocket.TextMessage, msgPayload); err != nil {
		done <- fmt.Errorf("client %d write: %w", clientID, err)
		return
	}

	readMessagesWithTimeout(conn, 100*time.Millisecond)
	done <- nil
}

// readMessagesWithTimeout reads messages from a connection with a timeout
func readMessagesWithTimeout(conn *websocket.Conn, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_, _, err := conn.ReadMessage()
				if err != nil {
					return
				}
			}
		}
	}()

	<-ctx.Done()
}

// waitForConcurrentClients waits for all concurrent clients to complete
func waitForConcurrentClients(t *testing.T, numClients int, done chan error) {
	t.Helper()

	for i := range numClients {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Client %d failed: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Errorf("Client %d timed out", i)
		}
	}
}

func TestWebSocketOriginValidation(t *testing.T) {
	t.Parallel()

	const allowedOrigin = "http://allowed.test"

	testServer, _ := newConfiguredTestServer(t, func(cfg *server.Config) {
		cfg.AllowedOrigins = append(cfg.AllowedOrigins, allowedOrigin)
	})

	wsURL := testServer.wsURL()

	t.Run("Allowed origin", func(t *testing.T) {
		testAllowedOrigin(t, wsURL, allowedOrigin)
	})

	t.Run("Disallowed origin", func(t *testing.T) {
		testDisallowedOrigin(t, wsURL)
	})
}

// testAllowedOrigin verifies that connections from allowed origins succeed
func testAllowedOrigin(t *testing.T, wsURL, allowedOrigin string) {
	t.Helper()

	header := http.Header{}
	header.Set("Origin", allowedOrigin)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("Expected allowed origin to succeed: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		if resp != nil {
			_ = resp.Body.Close()
		}
	})
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("Expected status %d, got %d", http.StatusSwitchingProtocols, resp.StatusCode)
	}
}

// testDisallowedOrigin verifies that connections from disallowed origins are rejected
func testDisallowedOrigin(t *testing.T, wsURL string) {
	t.Helper()

	header := http.Header{}
	header.Set("Origin", "http://blocked.test")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err == nil {
		_ = conn.Close()
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("Expected disallowed origin to fail")
	}
	if resp == nil {
		t.Fatalf("Expected HTTP response for disallowed origin")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("Expected status %d for disallowed origin, got %d", http.StatusForbidden, resp.StatusCode)
	}
}

func TestWebSocketMessageSizeLimit(t *testing.T) {
	t.Parallel()

	const limit int64 = 64

	testServer, hub := newConfiguredTestServer(t, func(cfg *server.Config) {
		cfg.MaxMessageSize = limit
	})

	sender, receiver := dialPair(t, hub, testServer.wsURL(), testServer.URL)

	oversizedContent := strings.Repeat("A", int(limit)+10)
	oversizedPayload := mustMarshalMessage(t, oversizedContent)
	if int64(len(oversizedPayload)) <= limit {
		t.Fatalf("Test payload is not oversized: %d bytes", len(oversizedPayload))
	}

	if err := sender.WriteMessage(websocket.TextMessage, oversizedPayload); err != nil && !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("Unexpected error writing oversized message: %v", err)
	}

	expectNoMessage(t, receiver, 200*time.Millisecond)

	if err := sender.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf(errMsgReadDeadline, err)
	}
	if _, _, readErr := sender.ReadMessage(); readErr == nil {
		t.Fatalf("Expected connection closure after oversized message")
	}
}

// TestWebSocketRateLimiting checks the limiter is wired into the read pump over
// a real socket: the burst is the one the hub was configured with, an over-limit
// frame is dropped rather than closing the connection, and tokens come back on
// their own. It is deliberately no longer the specification of refill — the
// arithmetic is pinned exactly, and without sleeping, by the limiter's unit
// tests in internal/server, which drive its clock directly.
func TestWebSocketRateLimiting(t *testing.T) {
	t.Parallel()

	rateCfg := server.RateLimitConfig{Burst: 2, RefillInterval: 500 * time.Millisecond}

	testServer, hub := newConfiguredTestServer(t, func(cfg *server.Config) {
		cfg.RateLimit = rateCfg
	})

	wsURL := testServer.wsURL()
	sender, receiver := dialPair(t, hub, wsURL, testServer.URL)

	sendAndReceiveBurstMessages(t, sender, receiver, rateCfg.Burst)
	testOverLimitMessageRejected(t, sender, receiver)

	// A fresh receiver starts with an empty read buffer, so the message after
	// the refill is unambiguously the one this test is waiting for.
	receiver = reconnectReceiver(t, hub, wsURL, testServer.URL, receiver)
	testMessageAfterRefill(t, sender, receiver, rateCfg.RefillInterval)
}

// sendAndReceiveBurstMessages sends and receives messages up to the burst limit
func sendAndReceiveBurstMessages(t *testing.T, sender, receiver *websocket.Conn, burstLimit int) {
	t.Helper()

	for i := range burstLimit {
		content := fmt.Sprintf("msg-%d", i)
		sendAndVerifyMessage(t, sender, receiver, content, i)
	}
}

// sendAndVerifyMessage sends a message from sender and verifies receiver gets it
func sendAndVerifyMessage(t *testing.T, sender, receiver *websocket.Conn, content string, msgNum int) {
	t.Helper()

	if err := sender.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, content)); err != nil {
		t.Fatalf("Failed to send message %d: %v", msgNum, err)
	}

	if err := receiver.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf(errMsgReadDeadline, err)
	}

	_, raw, err := receiver.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to receive message %d: %v", msgNum, err)
	}

	var msg server.Message
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("Failed to unmarshal message %d: %v", msgNum, err)
	}

	if msg.Content != content {
		t.Fatalf("Expected content %q, got %q", content, msg.Content)
	}
}

// testOverLimitMessageRejected verifies that messages over the rate limit are rejected
func testOverLimitMessageRejected(t *testing.T, sender, receiver *websocket.Conn) {
	t.Helper()

	if err := sender.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, "over-limit")); err != nil {
		t.Fatalf("Failed to send over-limit message: %v", err)
	}
	expectNoMessage(t, receiver, 200*time.Millisecond)
}

// reconnectReceiver closes the receiver, waits for the hub to drop it, and dials
// a replacement that the hub has registered before this returns.
func reconnectReceiver(t *testing.T, hub *server.Hub, wsURL, serverURL string, oldReceiver *websocket.Conn) *websocket.Conn {
	t.Helper()

	_ = oldReceiver.Close()
	waitForUnregister(t, hub, 1)

	return dial(t, hub, wsURL, serverURL)
}

// testMessageAfterRefill verifies that messages get through again once the
// bucket has refilled. The unit tests drive the limiter from a fixed instant, so
// this is the one check that a connection's tokens come back in real time —
// end to end, over a socket, on the clock production actually runs on. That is
// why the sleep stays.
func testMessageAfterRefill(t *testing.T, sender, receiver *websocket.Conn, refillInterval time.Duration) {
	t.Helper()

	// Not a synchronization sleep: reaching the limiter through a real socket
	// means going through newClient, which builds a limiter on the real clock,
	// so wall-clock time is the only clock this test can advance.
	time.Sleep(refillInterval + 100*time.Millisecond)

	if err := sender.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, "after-refill")); err != nil {
		t.Fatalf("Failed to send message after refill: %v", err)
	}

	waitForSpecificMessage(t, receiver, "after-refill", 2*time.Second)
}

// waitForSpecificMessage waits for a specific message content to be received
func waitForSpecificMessage(t *testing.T, receiver *websocket.Conn, expectedContent string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if err := receiver.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			t.Fatalf(errMsgReadDeadline, err)
		}

		_, raw, err := receiver.ReadMessage()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			t.Fatalf("Failed to receive message after refill: %v", err)
		}

		var msg server.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("Failed to unmarshal message after refill: %v", err)
		}

		if msg.Content == expectedContent {
			return
		}
	}

	t.Fatalf("Expected '%s' message after tokens refilled", expectedContent)
}
