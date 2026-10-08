// Package server exposes HTTP handlers, including WebSocket upgrades, health
// checks, and the built-in test page.
package server

import (
	_ "embed"
	"net/http"
	"strconv"
	"sync"

	"github.com/gorilla/websocket"
)

//go:embed testpage.html
var testPageHTML []byte

// HealthResponse is the exact body served by [healthHandler]. It is exported so
// tests assert against the served text rather than a copy of it.
const HealthResponse = "Blip server is running!"

var (
	healthResponse = []byte(HealthResponse)

	// testPageLength is precomputed so the handler does no per-request work
	// beyond writing the embedded bytes.
	testPageLength = strconv.Itoa(len(testPageHTML))
	healthLength   = strconv.Itoa(len(healthResponse))
)

// writeBufferPool lets gorilla/websocket share write buffers across
// connections instead of retaining one per client, which keeps memory flat as
// the connection count grows.
var writeBufferPool = &sync.Pool{}

// newUpgrader builds the upgrader for one hub's WebSocket handler. CheckOrigin is
// bound to that hub's origin policy, so the allow-list is per hub rather than per
// process; the write buffer pool is deliberately not, since sharing it is what
// keeps memory flat as the connection count grows.
func newUpgrader(origins originPolicy) websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		WriteBufferPool: writeBufferPool,
		CheckOrigin:     origins.checkOrigin,
	}
}

// webSocketHandlerForHub returns the handler for WebSocket upgrade requests
// against h. It validates that the request uses the GET method, upgrades the
// HTTP connection to WebSocket, creates a new client, and registers it
// with the hub, which starts the client's read/write pumps.
//
// Every connection runs under h's resolved settings: the upgrader checks h's
// origin policy, and each client gets h's size and rate limits.
func webSocketHandlerForHub(h *Hub) http.HandlerFunc {
	cfg := &h.cfg
	upgrader := newUpgrader(cfg.origins)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed. WebSocket endpoint only accepts GET requests.", http.StatusMethodNotAllowed)
			return
		}

		if r.Context().Err() != nil {
			log().Debug("websocket request cancelled before upgrade")
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log().Warn("websocket upgrade failed", "remote_addr", r.RemoteAddr, "error", err)
			return
		}

		client := newClient(conn, h, r.RemoteAddr, cfg)

		// A rejected client was never added to the hub, so closing it is the
		// handler's job.
		if !h.register(r.Context(), client) {
			client.closeConnection()
		}
	}
}

// writeStatic serves a fixed body whose length is known ahead of time, skipping
// the sniffing and chunking net/http would otherwise do. It is a no-op once the
// client has gone away.
func writeStatic(w http.ResponseWriter, r *http.Request, contentType, contentLength string, body []byte) {
	if r.Context().Err() != nil {
		return
	}

	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", contentLength)

	if _, err := w.Write(body); err != nil {
		log().Debug("error writing response", "path", r.URL.Path, "error", err)
	}
}

// healthHandler provides a simple health check endpoint that returns server status.
// It responds with a plain text message indicating the server is running.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, "text/plain", healthLength, healthResponse)
}

// testPageHandler serves an HTML page for exercising the WebSocket endpoint.
// It provides a simple web interface to connect to the WebSocket endpoint,
// send messages, and view real-time chat communication.
func testPageHandler(w http.ResponseWriter, r *http.Request) {
	writeStatic(w, r, "text/html; charset=utf-8", testPageLength, testPageHTML)
}
