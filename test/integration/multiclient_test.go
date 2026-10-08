// Package integration contains integration tests for multi-client scenarios.
//
// These tests verify the system behavior when multiple clients connect
// simultaneously, send messages, and interact with each other through
// the hub's broadcast system.
package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/maltemindedal/blip/internal/server"
)

const (
	msgAfterNewClientJoined = "After new client joined"
	msgFromClientTemplate   = "Message from client %d"
	msgInitial              = "Initial message"
)

// TestMultipleClientsMessageExchange tests complex message exchange scenarios
// between multiple clients connected to the hub.
func TestMultipleClientsMessageExchange(t *testing.T) {
	t.Parallel()

	t.Run("Five clients sending and receiving messages", func(t *testing.T) {
		t.Parallel()

		testServer, hub := newMulticlientServer(t)
		testFiveClientsSendingAndReceiving(t, hub, testServer.wsURL(), testServer.URL)
	})

	t.Run("Clients joining and leaving dynamically", func(t *testing.T) {
		t.Parallel()

		testServer, hub := newMulticlientServer(t)
		testDynamicJoiningAndLeaving(t, hub, testServer.wsURL(), testServer.URL)
	})

	t.Run("Rapid message exchange between clients", func(t *testing.T) {
		t.Parallel()

		testServer, hub := newMulticlientServer(t)
		testRapidMessageExchange(t, hub, testServer.wsURL(), testServer.URL)
	})
}

// newMulticlientServer starts a server whose rate limit is wide enough that the
// multi-client tests measure fan-out rather than throttling — rate limiting has
// its own coverage in security_test.go.
func newMulticlientServer(t *testing.T) (*testService, *server.Hub) {
	t.Helper()

	return newConfiguredTestServer(t, func(cfg *server.Config) {
		cfg.RateLimit = server.RateLimitConfig{Burst: 1000, RefillInterval: time.Second}
	})
}

// TestMultipleClientsConcurrentOperations tests concurrent operations with multiple clients.
func TestMultipleClientsConcurrentOperations(t *testing.T) {
	t.Parallel()

	t.Run("Concurrent client connections and disconnections", func(t *testing.T) {
		t.Parallel()

		testServer, _ := newMulticlientServer(t)
		testConcurrentConnectionsAndDisconnections(t, testServer.wsURL(), testServer.URL)
	})

	t.Run("Concurrent message sending from multiple clients", func(t *testing.T) {
		t.Parallel()

		testServer, hub := newMulticlientServer(t)
		testConcurrentMessageSending(t, hub, testServer.wsURL(), testServer.URL)
	})
}

// TestMultipleClientsEdgeCases tests edge cases with multiple clients.
func TestMultipleClientsEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("Single client broadcasting to itself", func(t *testing.T) {
		t.Parallel()

		testServer, hub := newMulticlientServer(t)
		conn := dial(t, hub, testServer.wsURL(), testServer.URL)

		// Send a message (should not receive it back)
		sendMessageFromClient(t, conn, "Self message")
		expectNoMessage(t, conn, 300*time.Millisecond)
	})

	t.Run("All clients disconnecting simultaneously", func(t *testing.T) {
		t.Parallel()

		testServer, hub := newMulticlientServer(t)

		const numClients = 5
		connections := dialClients(t, hub, testServer.wsURL(), testServer.URL, numClients)

		var wg sync.WaitGroup
		wg.Add(numClients)

		for i := range numClients {
			go func(clientID int) {
				defer wg.Done()
				if err := connections[clientID].Close(); err != nil {
					t.Logf("Client %d close error: %v", clientID, err)
				}
			}(i)
		}

		wg.Wait()
		waitForUnregister(t, hub, 0)
	})

	t.Run("Client sending empty content messages", func(t *testing.T) {
		t.Parallel()

		testServer, hub := newMulticlientServer(t)
		connections := dialClients(t, hub, testServer.wsURL(), testServer.URL, 2)

		// Send message with empty content
		sendMessageFromClient(t, connections[0], "")

		// Client 1 should receive it
		verifyClientReceivesMessage(t, connections[1], "", 1)
		expectNoMessage(t, connections[0], 150*time.Millisecond)
	})

	t.Run("Clients sending very long content", func(t *testing.T) {
		t.Parallel()

		testServer, hub := newMulticlientServer(t)
		connections := dialClients(t, hub, testServer.wsURL(), testServer.URL, 2)

		// Send a long message (but within size limit)
		longContent := strings.Repeat("X", 50)

		sendMessageFromClient(t, connections[0], longContent)
		verifyClientReceivesMessage(t, connections[1], longContent, 1)
		expectNoMessage(t, connections[0], 150*time.Millisecond)
	})
}

// drainMessages reads and discards all available messages from a connection
func drainMessages(conn *websocket.Conn, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
			break
		}
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// testFiveClientsSendingAndReceiving tests that five clients can send messages
// and all other clients receive them correctly.
func testFiveClientsSendingAndReceiving(t *testing.T, hub *server.Hub, wsURL, serverURL string) {
	t.Helper()

	const numClients = 5
	connections := dialClients(t, hub, wsURL, serverURL, numClients)

	// Each client sends a unique message
	sendMessagesFromAllClients(t, connections, numClients)

	// Verify each client received all messages except their own. The reads
	// carry their own deadlines, so nothing here has to guess a delivery delay.
	verifyAllClientsReceivedMessages(t, connections, numClients)
}

// sendMessagesFromAllClients sends one message from each client
func sendMessagesFromAllClients(t *testing.T, connections []*websocket.Conn, numClients int) {
	t.Helper()

	for i := range numClients {
		messageContent := fmt.Sprintf(msgFromClientTemplate, i)
		sendMessageFromClient(t, connections[i], messageContent)
	}
}

// verifyAllClientsReceivedMessages verifies each client received expected messages
func verifyAllClientsReceivedMessages(t *testing.T, connections []*websocket.Conn, numClients int) {
	t.Helper()

	expectedMessagesPerClient := numClients - 1

	for i := range numClients {
		messagesReceived := readAllMessagesFromClient(t, connections[i], expectedMessagesPerClient, i)
		verifyReceivedMessageCount(t, messagesReceived, expectedMessagesPerClient, i)
		verifyDidNotReceiveOwnMessage(t, messagesReceived, i)
	}
}

// readAllMessagesFromClient reads all available messages for a client
func readAllMessagesFromClient(t *testing.T, conn *websocket.Conn, expectedCount, clientIndex int) map[string]bool {
	t.Helper()

	messagesReceived := make(map[string]bool)
	deadline := time.Now().Add(2 * time.Second)

	for len(messagesReceived) < expectedCount && time.Now().Before(deadline) {
		messages := readSingleWebSocketMessage(t, conn, clientIndex)
		if messages == nil {
			break
		}
		for _, content := range messages {
			messagesReceived[content] = true
		}
	}

	return messagesReceived
}

// readSingleWebSocketMessage reads one WebSocket message and returns all contained messages
func readSingleWebSocketMessage(t *testing.T, conn *websocket.Conn, clientIndex int) []string {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Errorf("Client %d: Failed to set read deadline: %v", clientIndex, err)
		return nil
	}

	messageType, message, err := conn.ReadMessage()
	if err != nil {
		return nil
	}

	if messageType != websocket.TextMessage {
		return nil
	}

	return parseMessageContent(message)
}

// parseMessageContent parses batched messages separated by newlines
func parseMessageContent(message []byte) []string {
	var contents []string
	parts := bytes.SplitSeq(message, []byte("\n"))

	for part := range parts {
		if len(part) == 0 {
			continue
		}
		var msg server.Message
		if err := json.Unmarshal(part, &msg); err == nil {
			contents = append(contents, msg.Content)
		}
	}

	return contents
}

// verifyReceivedMessageCount checks if the client received the expected number of messages
func verifyReceivedMessageCount(t *testing.T, messagesReceived map[string]bool, expected, clientIndex int) {
	t.Helper()

	if len(messagesReceived) != expected {
		t.Errorf("Client %d: Expected %d messages, got %d", clientIndex, expected, len(messagesReceived))
	}
}

// verifyDidNotReceiveOwnMessage checks that a client didn't receive its own message
func verifyDidNotReceiveOwnMessage(t *testing.T, messagesReceived map[string]bool, clientIndex int) {
	t.Helper()

	ownMessage := fmt.Sprintf(msgFromClientTemplate, clientIndex)
	if messagesReceived[ownMessage] {
		t.Errorf("Client %d received its own message", clientIndex)
	}
}

// testDynamicJoiningAndLeaving tests clients connecting and disconnecting
// dynamically while messages are being sent.
func testDynamicJoiningAndLeaving(t *testing.T, hub *server.Hub, wsURL, serverURL string) {
	t.Helper()

	// Start with 3 clients
	connections := dialClients(t, hub, wsURL, serverURL, 3)

	// Client 0 sends a message
	sendMessageFromClient(t, connections[0], msgInitial)

	// Verify clients 1 and 2 received the message
	verifyClientReceivesMessage(t, connections[1], msgInitial, 1)
	verifyClientReceivesMessage(t, connections[2], msgInitial, 2)

	// Client 1 disconnects, and the hub confirms it is gone before the next send
	closeClientConnection(t, connections, 1)
	waitForUnregister(t, hub, 2)

	// Client 0 sends another message (only client 2 should receive)
	sendMessageFromClient(t, connections[0], "After client 1 left")
	verifyClientReceivesMessage(t, connections[2], "After client 1 left", 2)

	// New client joins
	newClient := dial(t, hub, wsURL, serverURL)

	// Client 2 sends a message (both client 0 and new client should receive)
	sendMessageFromClient(t, connections[2], msgAfterNewClientJoined)

	// Client 0 may still have the earlier broadcast queued, so it needs the
	// variant that scans past messages it has already been sent.
	verifyClientReceivesMessageFlexible(t, connections[0], msgAfterNewClientJoined, 0)
	verifyClientReceivesMessage(t, newClient, msgAfterNewClientJoined, 3)
	expectNoMessage(t, connections[2], 200*time.Millisecond)

	// Clean up remaining connections
	closeRemainingConnections(t, connections)
}

// verifyClientReceivesMessageFlexible is a more flexible version that handles
// potential timing issues and batched messages
func verifyClientReceivesMessageFlexible(t *testing.T, conn *websocket.Conn, expectedContent string, clientIndex int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)

	defer handlePanicDuringMessageRead(t, clientIndex)

	found := searchForMessageWithRetry(t, conn, expectedContent, clientIndex, deadline)

	if !found {
		t.Errorf("Client %d: Expected content %q not found after 3 seconds", clientIndex, expectedContent)
	}
}

// handlePanicDuringMessageRead recovers from panics during WebSocket reads
func handlePanicDuringMessageRead(t *testing.T, clientIndex int) {
	t.Helper()

	if r := recover(); r != nil {
		t.Errorf("Client %d: Panic while reading message: %v", clientIndex, r)
	}
}

// searchForMessageWithRetry searches for expected message content with retry logic
func searchForMessageWithRetry(t *testing.T, conn *websocket.Conn, expectedContent string, clientIndex int, deadline time.Time) bool {
	t.Helper()

	for time.Now().Before(deadline) {
		message, err := readWebSocketMessageWithTimeout(t, conn, clientIndex)
		if err != nil {
			if isFatalWebSocketError(err) {
				t.Errorf("Client %d: Connection closed while waiting for message: %v", clientIndex, err)
				return false
			}
			// Timeout is OK, we'll try again
			continue
		}

		if message == nil {
			continue
		}

		if messageContainsExpectedContent(message, expectedContent) {
			return true
		}
	}
	return false
}

// readWebSocketMessageWithTimeout reads a WebSocket message with a timeout
func readWebSocketMessageWithTimeout(t *testing.T, conn *websocket.Conn, clientIndex int) ([]byte, error) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Errorf("Client %d: Failed to set read deadline: %v", clientIndex, err)
		return nil, err
	}

	messageType, message, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	if messageType != websocket.TextMessage {
		return nil, nil
	}

	return message, nil
}

// isFatalWebSocketError checks if the error is a fatal WebSocket connection error
func isFatalWebSocketError(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) ||
		websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure)
}

// messageContainsExpectedContent checks if batched message contains expected content
func messageContainsExpectedContent(message []byte, expectedContent string) bool {
	parts := bytes.SplitSeq(message, []byte("\n"))

	for part := range parts {
		if len(part) == 0 {
			continue
		}

		var received server.Message
		if err := json.Unmarshal(part, &received); err != nil {
			continue
		}

		if received.Content == expectedContent {
			return true
		}
	}

	return false
}

// testRapidMessageExchange tests multiple clients sending messages rapidly
// and verifies all messages are received correctly.
func testRapidMessageExchange(t *testing.T, hub *server.Hub, wsURL, serverURL string) {
	t.Helper()

	const numClients = 3
	connections := dialClients(t, hub, wsURL, serverURL, numClients)

	// Send multiple messages rapidly from each client
	const messagesPerClient = 5
	sendRapidMessages(t, connections, messagesPerClient)

	// Every message is delivered or the sender is dropped, so the count is
	// exact: no client should be missing any of the other clients' messages.
	expectedMessagesPerClient := messagesPerClient * (numClients - 1)

	for clientID := range numClients {
		receivedCount := countReceivedMessages(t, connections[clientID], expectedMessagesPerClient)

		if receivedCount != expectedMessagesPerClient {
			t.Errorf("Client %d: expected %d messages, got %d",
				clientID, expectedMessagesPerClient, receivedCount)
		}
	}
}

// sendRapidMessages sends multiple messages rapidly from each client.
func sendRapidMessages(t *testing.T, connections []*websocket.Conn, messagesPerClient int) {
	t.Helper()

	numClients := len(connections)
	for round := range messagesPerClient {
		for clientID := range numClients {
			content := fmt.Sprintf("Round %d from client %d", round, clientID)
			sendMessageFromClient(t, connections[clientID], content)
		}
	}
}

// countReceivedMessages counts how many valid messages a client receives
// within a timeout period. Handles batched messages separated by newlines.
func countReceivedMessages(t *testing.T, conn *websocket.Conn, maxExpected int) int {
	t.Helper()

	receivedCount := 0
	deadline := time.Now().Add(5 * time.Second)

	for receivedCount < maxExpected && time.Now().Before(deadline) {
		message, err := readSingleMessageWithDeadline(t, conn)
		if err != nil {
			break
		}

		if message != nil {
			receivedCount += countMessagesInBatch(message)
		}
	}

	return receivedCount
}

// readSingleMessageWithDeadline reads a single WebSocket message with a deadline
func readSingleMessageWithDeadline(t *testing.T, conn *websocket.Conn) ([]byte, error) {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		t.Logf("Failed to set read deadline: %v", err)
		return nil, err
	}

	messageType, message, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	if messageType != websocket.TextMessage {
		return nil, nil
	}

	return message, nil
}

// countMessagesInBatch counts valid messages in a batched message payload
func countMessagesInBatch(message []byte) int {
	count := 0
	parts := bytes.SplitSeq(message, []byte("\n"))

	for part := range parts {
		if len(part) == 0 {
			continue
		}

		var msg server.Message
		if err := json.Unmarshal(part, &msg); err == nil {
			count++
		}
	}

	return count
}

// closeClientConnection safely closes a client connection at the given index.
func closeClientConnection(t *testing.T, connections []*websocket.Conn, index int) {
	t.Helper()

	if err := connections[index].Close(); err != nil {
		t.Errorf("Failed to close client %d: %v", index, err)
	}
	connections[index] = nil
}

// closeRemainingConnections closes all non-nil connections in the slice.
func closeRemainingConnections(t *testing.T, connections []*websocket.Conn) {
	t.Helper()

	for i, conn := range connections {
		if conn != nil {
			if err := conn.Close(); err != nil {
				t.Logf("Failed to close connection %d: %v", i, err)
			}
		}
	}
}

// testConcurrentConnectionsAndDisconnections tests multiple clients connecting
// and disconnecting concurrently.
func testConcurrentConnectionsAndDisconnections(t *testing.T, wsURL, serverURL string) {
	t.Helper()

	const numClients = 10
	var wg sync.WaitGroup
	errors := make(chan error, numClients)

	wg.Add(numClients)
	for i := range numClients {
		go runSingleConcurrentClient(t, wsURL, serverURL, i, &wg, errors)
	}

	wg.Wait()
	close(errors)

	reportErrors(t, errors)
}

// runSingleConcurrentClient connects a single client, sends a message, reads responses,
// and disconnects.
func runSingleConcurrentClient(t *testing.T, wsURL, serverURL string, clientID int, wg *sync.WaitGroup, errors chan<- error) {
	t.Helper()

	defer wg.Done()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, newOriginHeader(serverURL))
	if err != nil {
		errors <- fmt.Errorf("client %d: connection failed: %w", clientID, err)
		return
	}
	defer func() { _ = conn.Close() }()
	defer func() { _ = resp.Body.Close() }()

	// Send a message
	content := fmt.Sprintf(msgFromClientTemplate, clientID)
	if err := conn.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, content)); err != nil {
		errors <- fmt.Errorf("client %d: send failed: %w", clientID, err)
		return
	}

	// Try to read some messages (may or may not receive)
	attemptToReadMessages(conn, 500*time.Millisecond)
}

// attemptToReadMessages attempts to read messages from a connection
// within the specified timeout period.
func attemptToReadMessages(conn *websocket.Conn, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			break
		}
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// testConcurrentMessageSending tests multiple clients sending messages concurrently.
func testConcurrentMessageSending(t *testing.T, hub *server.Hub, wsURL, serverURL string) {
	t.Helper()

	const numClients = 5
	connections := dialClients(t, hub, wsURL, serverURL, numClients)

	errors := sendMessagesFromAllClientsConcurrently(t, connections)
	reportErrors(t, errors)

	// Drain messages from all clients
	drainAllClientMessages(connections)
}

// sendMessagesFromAllClientsConcurrently sends multiple messages from each client
// concurrently and returns any errors that occurred.
func sendMessagesFromAllClientsConcurrently(t *testing.T, connections []*websocket.Conn) chan error {
	t.Helper()

	const messagesPerClient = 10
	numClients := len(connections)

	var wg sync.WaitGroup
	errors := make(chan error, numClients*messagesPerClient)

	// Each client sends 10 messages concurrently
	for i := range numClients {
		wg.Add(1)
		go sendMultipleMessagesFromClient(t, connections[i], i, messagesPerClient, &wg, errors)
	}

	wg.Wait()
	close(errors)

	return errors
}

// sendMultipleMessagesFromClient sends multiple messages from a single client.
func sendMultipleMessagesFromClient(t *testing.T, conn *websocket.Conn, clientID, numMessages int, wg *sync.WaitGroup, errors chan<- error) {
	t.Helper()

	defer wg.Done()

	for msgNum := range numMessages {
		content := fmt.Sprintf("Client %d message %d", clientID, msgNum)
		if err := conn.WriteMessage(websocket.TextMessage, mustMarshalMessage(t, content)); err != nil {
			errors <- fmt.Errorf("client %d msg %d: send failed: %w", clientID, msgNum, err)
		}
	}
}

// drainAllClientMessages drains messages from all client connections. Each drain
// reads until its own deadline passes with nothing left to read, so it does not
// need a delay in front of it.
func drainAllClientMessages(connections []*websocket.Conn) {
	for i := range connections {
		drainMessages(connections[i], 1*time.Second)
	}
}

// reportErrors reports all errors from the error channel to the test.
func reportErrors(t *testing.T, errors <-chan error) {
	t.Helper()

	for err := range errors {
		t.Error(err)
	}
}
