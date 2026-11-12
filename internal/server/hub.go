// Package server coordinates client registration, message broadcast, and
// connection cleanup for the Blip WebSocket system via the Hub type.
package server

import (
	"context"
	"fmt"
	"sync"
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
// A Hub has two states, running and stopped, and [startHub] is the only way to
// get one: it returns with the run loop already launched, so there is no
// constructed-but-unstarted hub to shut down early, to forget to start, or to
// block on. [Hub.shutdown] is the only transition. The zero value is still not
// usable: its channels are never made, so [Hub.ClientCount] on one would block
// forever. No code path builds one.
//
// The set of connected clients is not a field of the Hub. It is a [registry]
// that lives in a local variable of the run loop, so no other goroutine can
// name it, let alone touch it: registration, unregistration, fan-out and the
// count all reach it through the hub's channels.
type Hub struct {
	// settings is what every connection of this hub runs under: its origin
	// allow-list, its message size limit, and its rate limit. It is set once by
	// [startHub] and never mutated, so the connection paths read it without
	// synchronization.
	settings settings

	broadcast     chan broadcastMessage
	registerReq   chan clientConn
	unregisterReq chan clientConn

	// countReq carries a reply channel into the run loop so callers can
	// read the client count without touching the registry themselves.
	countReq chan chan int

	// wg tracks one goroutine per registered client. Only the run loop adds
	// to it, so every client that got in before quit closed is waited for.
	wg sync.WaitGroup

	quit         chan struct{}
	shutdownOnce sync.Once
	done         chan struct{}
}

// startHub builds a hub running under s and launches its run loop. The caller
// owns stopping it with [Hub.shutdown]: [Service] does that on every way out of
// Run and Serve, and a test gets its hub from startTestHub, which registers the
// shutdown. A hub nobody shuts down fails the test binary (see TestMain).
func startHub(s settings) *Hub {
	h := &Hub{
		settings:      s,
		broadcast:     make(chan broadcastMessage),
		registerReq:   make(chan clientConn),
		unregisterReq: make(chan clientConn),
		countReq:      make(chan chan int),
		quit:          make(chan struct{}),
		done:          make(chan struct{}),
	}

	go h.run()
	return h
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
// answered by the run loop, which owns the registry, so it is consistent with
// every registration and broadcast the hub has already processed. It returns 0
// once the hub has stopped.
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

// IsStopped reports whether the hub event loop has exited.
func (h *Hub) IsStopped() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// run is the hub's event loop. It owns the registry outright: the variable is
// local to this function, which [startHub] launches exactly once.
func (h *Hub) run() {
	defer close(h.done)

	reg := newRegistry()

	for {
		select {
		case <-h.quit:
			reg.closeAll()
			return

		case client := <-h.registerReq:
			reg.add(client)

			// Counted here, inside the run loop, so a client that got in
			// before shutdown was signalled is always one shutdown waits for.
			h.wg.Go(client.serve)

		case client := <-h.unregisterReq:
			reg.remove(client)

		case msg := <-h.broadcast:
			reg.handleBroadcast(msg)

		case reply := <-h.countReq:
			reply <- len(reg.clients)
		}
	}
}

// registry is the set of connected clients and the fan-out over it. It has no
// channels and no locks because exactly one goroutine uses a given registry:
// the hub's run loop keeps its own in a local variable.
type registry struct {
	// clients maps every registered client to the inbox it was registered with.
	// Keeping the channel here rather than asking the client for it per message
	// is what keeps the fan-out free of interface dispatch.
	clients map[clientConn]chan<- []byte

	// failed is scratch space reused across broadcasts to avoid allocating a
	// slice per message.
	failed []clientConn
}

func newRegistry() *registry {
	return &registry{clients: make(map[clientConn]chan<- []byte)}
}

// add records a client and the inbox the fan-out will send on.
func (r *registry) add(client clientConn) {
	r.clients[client] = client.inbox()
	log().Info("client registered", "addr", client.remoteAddr(), "total_clients", len(r.clients))
}

// remove forgets a client and closes its inbox. It is a no-op for clients that
// are already gone, which makes double unregistration safe.
func (r *registry) remove(client clientConn) {
	inbox, ok := r.clients[client]
	if !ok {
		return
	}

	delete(r.clients, client)
	close(inbox)
	log().Info("client unregistered", "addr", client.remoteAddr(), "total_clients", len(r.clients))
}

// handleBroadcast fans a message out to every client except the sender.
//
// Delivery is non-blocking: a client whose buffer is full is dropped rather
// than allowed to stall the hub for everyone else.
func (r *registry) handleBroadcast(msg broadcastMessage) {
	r.failed = r.failed[:0]
	delivered := 0

	for client, inbox := range r.clients {
		if client == msg.Sender {
			continue
		}

		select {
		case inbox <- msg.Payload:
			delivered++
		default:
			r.failed = append(r.failed, client)
		}
	}

	dropped := len(r.failed)
	for _, client := range r.failed {
		log().Warn("dropping client with a full send buffer", "addr", client.remoteAddr())
		r.remove(client)
	}
	clear(r.failed)

	if debugEnabled() {
		log().Debug("broadcast complete", "delivered", delivered, "dropped", dropped)
	}
}

// closeAll closes every client connection, which unblocks the read pumps so
// they can exit.
func (r *registry) closeAll() {
	for client := range r.clients {
		client.closeConnection()
	}

	log().Info("closed client connections", "count", len(r.clients))
	clear(r.clients)
}

// shutdown stops the hub and waits for its goroutines: the run loop, which
// closes every client connection on its way out, and then every client's pumps.
// It returns once they have finished or when ctx is done, whichever comes
// first; both waits share the one deadline carried by ctx. It is idempotent.
func (h *Hub) shutdown(ctx context.Context) error {
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
