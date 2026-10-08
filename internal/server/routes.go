// Package server wires HTTP handlers into a ServeMux for the Blip
// application via routing helpers.
package server

import "net/http"

// setupRoutes configures and returns an HTTP ServeMux bound to the provided hub.
// [New] wires the service's own hub through it; the package's tests use it to
// exercise the routes against a hub of their own.
func setupRoutes(h *Hub) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", healthHandler)
	mux.HandleFunc("/ws", webSocketHandlerForHub(h))
	mux.HandleFunc("/test", testPageHandler)
	return mux
}
