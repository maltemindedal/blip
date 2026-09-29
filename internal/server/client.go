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

// Client represents a WebSocket client connection in the chat system.
// It manages the connection state, message sending channel, hub reference,
// and client address information.
type Client struct {
	conn           *websocket.Conn
	send           chan []byte
	hub            *Hub
	addr           string
	maxMessageSize int64
	rateLimiter    rateLimiter
	rateLimit      RateLimitConfig
	throttled      bool // read pump only: a throttling episode has been logged
}

// NewClient creates a new Client instance with the provided WebSocket connection,
// hub reference, and client address. The client's send channel is buffered
// to handle message queuing.
//
// The size limit and the rate limit come from the hub the client is joining,
// which resolved them when it was built.
func NewClient(conn *websocket.Conn, hub *Hub, addr string) *Client {
	cfg := &hub.cfg
	conn.SetReadLimit(cfg.MaxMessageSize)

	return &Client{
		conn:           conn,
		send:           make(chan []byte, sendBufferSz),
		hub:            hub,
		addr:           addr,
		maxMessageSize: cfg.MaxMessageSize,
		rateLimiter:    newRateLimiter(cfg.RateLimit.Burst, cfg.RateLimit.RefillInterval),
		rateLimit:      cfg.RateLimit,
	}
}

// inbox is the channel the hub delivers into, and closes when it drops this
// client. It satisfies [clientConn].
func (c *Client) inbox() chan<- []byte { return c.send }

// remoteAddr is the address the hub names this client by in its log records.
// It satisfies [clientConn].
func (c *Client) remoteAddr() string { return c.addr }

// serve runs the connection's two pumps and returns once both have exited. It
// satisfies [clientConn], so the hub launches one goroutine per client and
// stays out of how many the connection actually needs — gorilla/websocket
// permits one concurrent reader and one concurrent writer, which is why there
// are two.
func (c *Client) serve() {
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		c.writePump()
	}()

	c.readPump()
	<-writeDone
}

// setupReadConnection configures read deadlines and the pong handler for the
// WebSocket connection.
func (c *Client) setupReadConnection() {
	if err := c.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		log().Warn("failed to set initial read deadline", "addr", c.addr, "error", err)
	}

	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})
}

// handleReadError logs the error at an appropriate level and always reports
// that the read loop should stop, since every read error is terminal.
func (c *Client) handleReadError(err error) bool {
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
func (c *Client) checkRateLimit() bool {
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
func (c *Client) processMessage(rawMessage []byte) bool {
	payload, err := normalizeMessage(rawMessage)
	if err != nil {
		log().Warn("invalid message", "addr", c.addr, "error", err)
		return false
	}

	if debugEnabled() {
		log().Debug("received message", "addr", c.addr, "payload", string(payload))
	}

	if !c.hub.Publish(BroadcastMessage{Sender: c, Payload: payload}) {
		log().Debug("skipping broadcast; hub is shutting down", "addr", c.addr)
		return false
	}

	return true
}

// cleanupReadPump handles cleanup tasks when readPump exits.
func (c *Client) cleanupReadPump() {
	c.hub.Unregister(c)
	c.closeConnection()
}

// handleReadMessage processes a single message read from the WebSocket and
// reports whether the read loop should stop.
func (c *Client) handleReadMessage() bool {
	_, rawMessage, err := c.conn.ReadMessage()
	if err != nil {
		return c.handleReadError(err)
	}

	if c.checkRateLimit() {
		c.processMessage(rawMessage)
	}

	return false
}

func (c *Client) readPump() {
	defer c.cleanupReadPump()

	c.setupReadConnection()

	for !c.handleReadMessage() { //nolint:revive // empty body is intentional
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.closeConnection()
	}()

	for c.processWriteEvent(ticker) {
	}
}

// processWriteEvent waits for the next write event and returns false when the
// pump should stop processing.
func (c *Client) processWriteEvent(ticker *time.Ticker) bool {
	select {
	case message, ok := <-c.send:
		return c.handleMessage(message, ok)
	case <-ticker.C:
		return c.handlePing()
	case <-c.hub.shutdown:
		return false
	}
}

// closeConnection safely closes the WebSocket connection with proper error handling.
func (c *Client) closeConnection() {
	if err := c.conn.Close(); err != nil && !isExpectedCloseError(err) {
		log().Debug("error closing connection", "addr", c.addr, "error", err)
	}
}

// handleMessage processes outgoing messages and returns false if the connection
// should be closed.
func (c *Client) handleMessage(message []byte, ok bool) bool {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		log().Debug("error setting write deadline", "addr", c.addr, "error", err)
		return false
	}

	if !ok {
		return c.writeCloseMessage()
	}

	return c.writeTextMessage(message)
}

// writeCloseMessage sends a close frame to the client.
func (c *Client) writeCloseMessage() bool {
	if err := c.conn.WriteMessage(websocket.CloseMessage, nil); err != nil && !isExpectedCloseError(err) {
		log().Debug("error writing close message", "addr", c.addr, "error", err)
	}

	return false
}

// writeTextMessage writes a text message, coalescing any frames already queued
// on the send channel into the same WebSocket frame to amortize syscalls.
func (c *Client) writeTextMessage(message []byte) bool {
	w, err := c.conn.NextWriter(websocket.TextMessage)
	if err != nil {
		log().Debug("error creating writer", "addr", c.addr, "error", err)
		return false
	}

	if !c.writeFrameBody(w, message) {
		// The frame is already broken; discard the writer without flushing it.
		_ = w.Close()
		return false
	}

	return c.closeWriter(w)
}

// writeFrameBody writes message followed by everything already queued on the
// send channel, newline-separated. It reports whether the whole frame was
// written.
func (c *Client) writeFrameBody(w io.Writer, message []byte) bool {
	if !c.writeChunk(w, message, "message") {
		return false
	}

	// Snapshot the depth once: anything queued after this point belongs to the
	// next frame.
	for range len(c.send) {
		queued, ok := <-c.send
		if !ok {
			return false
		}

		if !c.writeChunk(w, newline, "separator") || !c.writeChunk(w, queued, "queued message") {
			return false
		}
	}

	return true
}

// newline separates coalesced messages inside a single frame.
var newline = []byte{'\n'}

// writeChunk writes one span of bytes into the open frame, logging what failed.
func (c *Client) writeChunk(w io.Writer, chunk []byte, what string) bool {
	if _, err := w.Write(chunk); err != nil {
		log().Debug("error writing frame chunk", "addr", c.addr, "chunk", what, "error", err)
		return false
	}

	return true
}

// closeWriter flushes the frame to the connection.
func (c *Client) closeWriter(w io.Closer) bool {
	if err := w.Close(); err != nil {
		log().Debug("error closing writer", "addr", c.addr, "error", err)
		return false
	}

	return true
}

// handlePing sends a ping frame to keep the connection alive.
func (c *Client) handlePing() bool {
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
		log().Debug("error setting ping write deadline", "addr", c.addr, "error", err)
		return false
	}

	if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
		if !isExpectedCloseError(err) {
			log().Debug("error writing ping", "addr", c.addr, "error", err)
		}
		return false
	}

	return true
}
