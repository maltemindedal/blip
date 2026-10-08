// Package server coordinates client registration, message broadcast, and
// connection cleanup for the Blip WebSocket system via the Hub type.
package server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// clientConn is everything the hub needs from a connected client, and nothing
// else. [wsClient] satisfies it over a real socket; a test registers a fake,
// which is the only way to drive the paths a real connection cannot be made to
// take on demand — a send buffer that fills faster than it drains, above all.
type clientConn interface {
	// inbox is the buffered channel the hub fans messages into. The hub reads
	// it once, at registration, and keeps it alongside the client, so the
	// fan-out sends on a channel directly instead of through this interface.
	// The hub also closes it on unregistration, which is what tells the write
	// pump to send a close frame and stop.
	inbox() chan<- []byte

	// serve runs the client until its connection is finished. The hub calls it
	// on a goroutine tracked by the WaitGroup [Hub.shutdown] drains, so the
	// client's own goroutines are part of the shutdown wait.
	serve()

	// closeConnection closes the underlying connection, unblocking whatever is
	// parked on it. The hub calls it on every client when it shuts down.
	closeConnection()

	// remoteAddr identifies the client in the hub's log records.
	remoteAddr() string
}

// Hub manages all WebSocket client connections and handles message broadcasting.
//
// A Hub is built by the [Service] that owns it and reached through
// [Service.Hub]. Its zero value is not usable: its channels are never made, so
// [Hub.ClientCount] on one would block forever. Even the Service's own hub
// answers ClientCount only once its run loop has started, which [Service.Run]
// and [Service.Serve] do; until then ClientCount blocks.
//
// The clients map is owned exclusively by the hub's run loop: registration,
// unregistration, broadcast fan-out, and shutdown all happen there. That single
// ownership removes lock traffic from the broadcast path entirely, so every
// mutation of clients must be reached through the hub's register, unregister
// and publish methods, which hand the work to that goroutine over a channel.
type Hub struct {
	// cfg is the resolved configuration every connection of this hub runs
	// under: its origin allow-list, its message size limit, and its rate
	// limit. It is set once by [newHub] and never mutated, so the connection
	// paths read it without synchronization.
	cfg resolvedConfig

	// clients maps every registered client to the inbox it was registered with.
	// Keeping the channel here rather than asking the client for it per message
	// is what keeps the fan-out free of interface dispatch.
	clients       map[clientConn]chan<- []byte
	broadcast     chan broadcastMessage
	registerReq   chan clientConn
	unregisterReq chan clientConn

	// failed is scratch space reused across broadcasts to avoid allocating a
	// slice per message. Only the run loop touches it.
	failed []clientConn

	// countReq carries a reply channel into the run loop so callers can
	// read the client count without touching the map themselves.
	countReq chan chan int

	wg           sync.WaitGroup
	quit         chan struct{}
	shutdownOnce sync.Once
	done         chan struct{}
	started      atomic.Bool
}

// newHub creates and initializes a new Hub instance with all necessary channels
// and client map. The returned Hub is ready to manage WebSocket connections.
//
// cfg is resolved once, here, and belongs to the hub from then on: the origin
// check, the message size limit, and the rate limit every connection of this hub
// runs under all come from it. A nil cfg means the defaults, which is what a
// caller that does not care about any of them passes.
func newHub(cfg *Config) *Hub {
	return &Hub{
		cfg:           resolveConfig(cfg),
		clients:       make(map[clientConn]chan<- []byte),
		broadcast:     make(chan broadcastMessage),
		registerReq:   make(chan clientConn),
		unregisterReq: make(chan clientConn),
		countReq:      make(chan chan int),
		quit:          make(chan struct{}),
		done:          make(chan struct{}),
	}
}

// register hands client to the hub's run loop, which adds it to the client set
// and starts serving it, and reports whether it was accepted.
//
// It returns false without registering anything if the hub is shutting down —
// which closes every connection itself, so a late arrival must not join — or if
// ctx is done, meaning the request that brought the client in has gone away. In
// both cases the caller still owns closing the connection.
func (h *Hub) register(ctx context.Context, client clientConn) bool {
	select {
	case h.registerReq <- client:
		return true

	case <-h.quit:
		log().Info("rejected websocket client; hub is shutting down", "remote_addr", client.remoteAddr())
		return false

	case <-ctx.Done():
		return false
	}
}

// unregister removes client from the hub's client set, closing its inbox.
// It is a no-op once the hub is shutting down, which tears every client down
// itself, so a read pump exiting because of that shutdown does not block on a
// run loop that has stopped reading.
func (h *Hub) unregister(client clientConn) {
	select {
	case h.unregisterReq <- client:
	case <-h.quit:
	}
}

// publish hands msg to the hub's run loop for fan-out and reports whether it was
// accepted. It returns false once the hub is shutting down, rather than blocking
// forever on a loop that will never read the message.
//
// Acceptance means the hub took the message, not that anyone received it:
// delivery to each client is best-effort, and a client whose buffer is full is
// dropped instead of allowed to stall the fan-out.
func (h *Hub) publish(msg broadcastMessage) bool {
	select {
	case h.broadcast <- msg:
		return true
	case <-h.quit:
		return false
	}
}

// ClientCount reports how many clients are currently registered. The count is
// answered by the run loop, which owns the map, so it is consistent with
// every registration and broadcast the hub has already processed. It blocks
// until the run loop has started, which [Service.Run] and [Service.Serve] do,
// and returns 0 once the hub has stopped.
func (h *Hub) ClientCount() int {
	reply := make(chan int, 1)

	select {
	case h.countReq <- reply:
		return <-reply
	case <-h.done:
		return 0
	}
}

// stopping returns a channel that is closed once the hub begins shutting down.
// A client goroutine that blocks on anything the shutdown must interrupt selects
// on it. It is receive-only, so code holding what it returns cannot close or
// send on the channel the hub's own shutdown sequence depends on. That covers
// only what goes through stopping: the quit field is visible to the whole
// package, so code outside hub.go leaving it alone is a convention.
func (h *Hub) stopping() <-chan struct{} {
	return h.quit
}

// start launches the hub event loop in a goroutine if it is not already running.
//
// The started flag is set here, before the goroutine exists, not inside it: a
// [Hub.shutdown] issued straight after start must see the loop as started and
// signal it, rather than return early and leave a loop that starts afterwards to
// run for good.
func (h *Hub) start() {
	if h.started.CompareAndSwap(false, true) {
		go h.run()
	}
}

// IsStopped reports whether the hub event loop has exited.
func (h *Hub) IsStopped() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// run is the hub's main event loop, handling client registration,
// unregistration, and message broadcasting. It runs until shutdown is signalled
// and is launched in its own goroutine by [Hub.start].
func (h *Hub) run() {
	defer close(h.done)

	for {
		select {
		case <-h.quit:
			h.shutdownClients()
			return

		case client := <-h.registerReq:
			h.addClient(client)

		case client := <-h.unregisterReq:
			h.removeClient(client)

		case broadcastMsg := <-h.broadcast:
			h.handleBroadcast(broadcastMsg)

		case reply := <-h.countReq:
			reply <- len(h.clients)
		}
	}
}

// addClient registers a client and starts serving it.
//
// The WaitGroup is incremented here, inside the run loop, so a client that got
// in before shutdown was signalled is always one [Hub.shutdown] waits for.
func (h *Hub) addClient(client clientConn) {
	h.clients[client] = client.inbox()
	log().Info("client registered", "addr", client.remoteAddr(), "total_clients", len(h.clients))

	h.wg.Go(func() {
		client.serve()
	})
}

// removeClient unregisters a client and closes its inbox. It is a no-op for
// clients that are already gone, which makes double unregistration safe.
func (h *Hub) removeClient(client clientConn) {
	inbox, ok := h.clients[client]
	if !ok {
		return
	}

	delete(h.clients, client)
	close(inbox)
	log().Info("client unregistered", "addr", client.remoteAddr(), "total_clients", len(h.clients))
}

// handleBroadcast fans a message out to every client except the sender.
//
// Delivery is non-blocking: a client whose buffer is full is dropped rather
// than allowed to stall the hub for everyone else.
func (h *Hub) handleBroadcast(broadcastMsg broadcastMessage) {
	h.failed = h.failed[:0]
	delivered := 0

	for client, inbox := range h.clients {
		if client == broadcastMsg.Sender {
			continue
		}

		select {
		case inbox <- broadcastMsg.Payload:
			delivered++
		default:
			h.failed = append(h.failed, client)
		}
	}

	dropped := len(h.failed)
	for _, client := range h.failed {
		log().Warn("dropping client with a full send buffer", "addr", client.remoteAddr())
		h.removeClient(client)
	}
	clear(h.failed)

	if debugEnabled() {
		log().Debug("broadcast complete", "delivered", delivered, "dropped", dropped)
	}
}

// shutdownClients closes every active client connection, which unblocks the
// read pumps so they can exit.
func (h *Hub) shutdownClients() {
	for client := range h.clients {
		client.closeConnection()
	}

	log().Info("closed client connections", "count", len(h.clients))
	clear(h.clients)
}

// shutdown initiates graceful shutdown of the hub and waits for all goroutines
// to complete. It returns after all client connections are closed and goroutines
// have finished, or when ctx is done — whichever comes first. Both stages share
// the one deadline carried by ctx.
func (h *Hub) shutdown(ctx context.Context) error {
	if !h.started.Load() {
		return nil
	}

	log().Info("initiating hub shutdown")

	h.shutdownOnce.Do(func() {
		close(h.quit)
	})

	if err := awaitStage(ctx, h.done, "event loop"); err != nil {
		return err
	}

	clientsDone := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(clientsDone)
	}()

	if err := awaitStage(ctx, clientsDone, "client"); err != nil {
		return err
	}

	log().Info("hub shutdown completed")
	return nil
}

// awaitStage blocks until done closes or ctx is cancelled, naming the stage in
// the log line and the returned error.
func awaitStage(ctx context.Context, done <-chan struct{}, stage string) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		log().Error("hub shutdown timed out", "stage", stage)
		return fmt.Errorf("hub %s shutdown timed out: %w", stage, ctx.Err())
	}
}
