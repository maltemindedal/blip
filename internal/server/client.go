// Package server manages individual WebSocket clients, handling read/write
// pumps, rate limiting, and lifecycle control for each connection.
package server

import (
	"errors"
	"io"
	"time"

	"github.com/gorilla/websocket"
)

// Connection timing parameters. pingPeriod must stay below pongWait so a peer
// has time to answer before its read deadline expires.
const (
	pongWait     = 60 * time.Second
	pingPeriod   = (pongWait * 9) / 10
	writeWait    = 10 * time.Second
	sendBufferSz = 256
)

// client represents a WebSocket client connection in the chat system.
// It manages the connection state, message sending channel, hub reference,
// and client address information.
type client struct {
	conn           *websocket.Conn
	send           chan []byte
	hub            *Hub
	addr           string
	maxMessageSize int64
	rateLimiter    rateLimiter
	rateLimit      RateLimitConfig
	throttled      bool // read pump only: a throttling episode has been logged
}

// newClient creates a new client with the provided WebSocket connection,
// hub reference, and client address. The client's send channel is buffered
// to handle message queuing.
//
// maxMessageSize and rateLimit are the limits the connection runs under; the
// caller passes the ones the hub resolved. Nothing here touches conn or hub, so
// a test can build a client without either and still get the limiter and the
// limits the read pump would, from the same arguments.
func newClient(conn *websocket.Conn, hub *Hub, addr string, maxMessageSize int64, rateLimit RateLimitConfig) *client {
	return &client{
		conn:           conn,
		send:           make(chan []byte, sendBufferSz),
		hub:            hub,
		addr:           addr,
		maxMessageSize: maxMessageSize,
		rateLimiter:    newRateLimiter(rateLimit.Burst, rateLimit.RefillInterval),
		rateLimit:      rateLimit,
	}
}

// inbox is the channel the hub delivers into, and closes when it drops this
// client. It satisfies [clientConn].
func (c *client) inbox() chan<- []byte { return c.send }

// remoteAddr is the address the hub names this client by in its log records.
// It satisfies [clientConn].
func (c *client) remoteAddr() string { return c.addr }

// serve runs the connection's two pumps and returns once both have exited. It
// satisfies [clientConn], so the hub launches one goroutine per client and
// stays out of how many the connection actually needs — gorilla/websocket
// permits one concurrent reader and one concurrent writer, which is why there
// are two.
func (c *client) serve() {
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		c.writePump()
	}()

	c.readPump()
	<-writeDone
}

// setupReadConnection configures the message size limit, read deadlines, and the
// pong handler for the WebSocket connection. It runs on the read pump before the
// first read, which is the only goroutine that reads.
func (c *client) setupReadConnection() {
	c.conn.SetReadLimit(c.maxMessageSize)

	if err := c.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log().Warn("failed to set initial read deadline", "addr", c.addr, "error", err)
	}

	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
}

// handleReadError logs the error at an appropriate level and always reports
// that the read loop should stop, since every read error is terminal.
func (c *client) handleReadError(err error) bool {
	if err == nil {
		return false
	}

	switch {
	case errors.Is(err, websocket.ErrReadLimit):
		log().Warn("message exceeded maximum size",
			"addr", c.addr, "max_bytes", c.maxMessageSize)

	case websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseAbnormalClosure),
		isExpectedCloseError(err):
		log().Debug("client disconnected", "addr", c.addr, "error", err)

	default:
		log().Warn("websocket read error", "addr", c.addr, "error", err)
	}

	return true
}

// checkRateLimit reports whether the client is within its message budget. Only
// the first discard of a throttling episode is logged, so a client flooding past
// its burst cannot make the server write a log line per frame it sends.
func (c *client) checkRateLimit() bool {
	if c.rateLimiter.allow() {
		c.throttled = false
		return true
	}

	if c.throttled {
		return false
	}
	c.throttled = true

	log().Warn("rate limit exceeded; discarding message",
		"addr", c.addr,
		"burst", c.rateLimit.Burst,
		"interval", c.rateLimit.RefillInterval)
	return false
}

// processMessage normalizes a raw frame and hands it to the hub for broadcast.
func (c *client) processMessage(rawMessage []byte) bool {
	payload, err := normalizeMessage(rawMessage)
	if err != nil {
		log().Warn("invalid message", "addr", c.addr, "error", err)
		return false
	}

	if debugEnabled() {
		log().Debug("received message", "addr", c.addr, "payload", string(payload))
	}

	if !c.hub.publish(broadcastMessage{Sender: c, Payload: payload}) {
		log().Debug("skipping broadcast; hub is shutting down", "addr", c.addr)
		return false
	}

	return true
}

// cleanupReadPump handles cleanup tasks when readPump exits.
func (c *client) cleanupReadPump() {
	c.hub.unregister(c)
	c.closeConnection()
}

// handleReadMessage processes a single message read from the WebSocket and
// reports whether the read loop should stop.
func (c *client) handleReadMessage() bool {
	_, rawMessage, err := c.conn.ReadMessage()
	if err != nil {
		return c.handleReadError(err)
	}

	if c.checkRateLimit() {
		c.processMessage(rawMessage)
	}

	return false
}

func (c *client) readPump() {
	defer c.cleanupReadPump()

	c.setupReadConnection()

	for !c.handleReadMessage() { //nolint:revive // empty body is intentional
	}
}

// writePump is the connection's only writer. It sends what the hub delivers,
// coalescing a burst into one frame, pings the peer every pingPeriod, and sends
// a close frame once the hub closes the inbox. It stops at the first write that
// fails, or as soon as the hub begins shutting down, and closes the connection
// either way.
func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.closeConnection()
	}()

	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				// The hub closed the inbox: it dropped or unregistered this
				// client.
				c.writeControl(websocket.CloseMessage, "close")
				return
			}

			if !c.writeMessages(message) {
				return
			}

		case <-ticker.C:
			if !c.writeControl(websocket.PingMessage, "ping") {
				return
			}

		case <-c.hub.stopping():
			return
		}
	}
}

// closeConnection safely closes the WebSocket connection with proper error handling.
func (c *client) closeConnection() {
	if err := c.conn.Close(); err != nil && !isExpectedCloseError(err) {
		log().Debug("error closing connection", "addr", c.addr, "error", err)
	}
}

// extendWriteDeadline gives the next write writeWait to complete, and reports
// whether the deadline could be set.
func (c *client) extendWriteDeadline() bool {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		log().Debug("error setting write deadline", "addr", c.addr, "error", err)
		return false
	}

	return true
}

// writeControl sends a close or ping frame with no payload, and reports whether
// it was written. name identifies the frame in the debug log's frame attribute.
func (c *client) writeControl(messageType int, name string) bool {
	if !c.extendWriteDeadline() {
		return false
	}

	if err := c.conn.WriteMessage(messageType, nil); err != nil {
		if !isExpectedCloseError(err) {
			log().Debug("error writing control frame", "addr", c.addr, "frame", name, "error", err)
		}
		return false
	}

	return true
}

// writeMessages sends first, and every message already queued behind it, as one
// text frame, and reports whether the frame was written.
func (c *client) writeMessages(first []byte) bool {
	if !c.extendWriteDeadline() {
		return false
	}

	w, err := c.conn.NextWriter(websocket.TextMessage)
	if err != nil {
		log().Debug("error creating writer", "addr", c.addr, "error", err)
		return false
	}

	if err = writeFrame(w, first, c.send); err != nil {
		log().Debug("error writing frame", "addr", c.addr, "error", err)
		// The writer keeps a failed write's error and returns it from Close
		// without flushing, so this only releases the writer.
		_ = w.Close()
		return false
	}

	if err = w.Close(); err != nil {
		log().Debug("error closing writer", "addr", c.addr, "error", err)
		return false
	}

	return true
}

// newline separates coalesced messages inside a single frame. Clients split a
// frame on it before parsing, so it is part of the wire format.
var newline = []byte{'\n'}

// writeFrame writes the body of one text frame into w: first, then every
// message already queued, separated by newlines. It returns the first write
// error.
//
// The queue's depth is read once, after first is written, so messages that
// arrive after that go into the next frame, and the frame never waits on an
// empty queue. Every receive below gets a message that was buffered when the
// depth was read: the write pump is the queue's only receiver, and closing a
// channel keeps what it buffered. So a hub that closes the inbox mid-frame still
// has the messages queued before it did delivered; the pump sees the close on
// its next receive.
func writeFrame(w io.Writer, first []byte, queued <-chan []byte) error {
	if _, err := w.Write(first); err != nil {
		return err
	}

	for range len(queued) {
		if _, err := w.Write(newline); err != nil {
			return err
		}

		if _, err := w.Write(<-queued); err != nil {
			return err
		}
	}

	return nil
}
