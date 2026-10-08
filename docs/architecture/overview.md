# Architecture Overview

Blip is a single-process, in-memory WebSocket broadcast server: one binary, one dependency
(`gorilla/websocket`), no database, no message history, no authentication. Everything below explains
how the pieces fit and why they are shaped that way.

## System context

```mermaid
flowchart LR
    B[Browser / client app] -- wss:// --> P[Reverse proxy<br/>TLS termination]
    P -- ws:// --> S

    subgraph S[Blip process]
        H[HTTP server<br/>ServeMux]
        HUB[Hub<br/>single goroutine]
        C1[Client A<br/>read + write pump]
        C2[Client B<br/>read + write pump]
        H -->|upgrade| C1
        H -->|upgrade| C2
        C1 <--> HUB
        C2 <--> HUB
    end
```

The proxy is not optional in production: the process speaks plain HTTP and has no authentication.
See [Deploying to production](../guides/deploying-to-production.md).

## Code layout

```
cmd/server/main.go          Entry point: logger, config, signal handling, Service.Run
internal/server/
  service.go                The service lifecycle: build, run, and drain in order
  config.go                 Env parsing, defaults, sanitization, the resolved config a hub owns
  logging.go                Structured log/slog logger and LOG_LEVEL handling
  routes.go                 ServeMux wiring for /, /ws, /test
  handlers.go               Upgrade handler, health handler, embedded test page
  testpage.html             The /test page, compiled into the binary with go:embed
  hub.go                    Client registry, broadcast fan-out, shutdown coordination
  client.go                 Per-connection read and write pumps
  origin.go                 Origin normalization and allow-list checks
  rate_limiter.go           Per-connection token bucket
  types.go                  Message payloads exchanged over the wire
  close_errors.go           Tells ordinary connection teardown from a real fault
  message_json.go           The wire-format encoder, kept identical to encoding/json
test/                       Integration suite and its helpers; unit tests sit beside the code
                            as *_internal_test.go (see guides/testing.md)
```

`internal/` means the packages cannot be imported by other modules — this is an application, not a
library. The `server` package is deliberately flat: every file declares `package server`, and the
package comment on each file describes that file's slice of responsibility.

## Components

**Service** (`service.go`) — the whole running server behind `New` and `Run`. `New(cfg)` builds the
hub, the routes bound to it, and the `http.Server` that fronts them; `Run(ctx)` starts them on the
configured port and drains them when `ctx` is cancelled. `Serve(ctx, ln)` is the same lifecycle on a
listener the caller has already opened, for a caller that needs the address first. Nothing else is exported from the lifecycle, so the shutdown
ordering below cannot be got wrong by a caller — including a test, which is why the tests drive the
real thing rather than a copy of it.

**Hub** (`hub.go`) — owns the set of connected clients and the configuration they run under.
`NewHub(cfg)` resolves the configuration once and keeps it, so the origin allow-list, the message
size limit, and the rate limit belong to that hub rather than to the process. `Start()` launches a
single goroutine — the hub's run loop — which selects over five channels: `register`, `unregister`,
`broadcast`, `countReq`, and `shutdown`.

That goroutine is the *sole* owner of the client map: registration, unregistration, fan-out, and
shutdown all happen inside the run loop, so the map needs no lock at all. Broadcasts iterate the map
directly instead of copying it, and the slice of failed clients is scratch space reused across
messages, which makes the fan-out path allocation-free. The rule that keeps this sound is simple:
every mutation of the client set must arrive through one of the hub's channels.

What that map holds is not a `*Client` but `clientConn`, the hub's own four-method view of one: an
inbox to deliver into, a `serve` to run, a connection to close at shutdown, and an address to log.
The client satisfies it over a real socket; a test registers a fake with no socket at all, which is
the only practical way to reach the drop-on-full rule below — see [Testing](../guides/testing.md).
The map's *value* is that inbox, read once when the client registers, so the fan-out sends on a
channel it already has rather than calling back through the interface for every client on every
message. Naming the seam therefore costs the broadcast path nothing: it measures slightly faster at
1000 clients than dereferencing the channel out of each client did, because the channel now sits in
the map next to the key instead of one pointer hop away in the client's own memory.

Those channels are the hub's own, though — nothing outside it sends on them. The one signal a client
does need, that shutdown has begun, it reads through `stopping()`, which hands out the `shutdown`
channel receive-only, so the compiler rather than convention keeps the client from closing or sending
on it. What the hub exports is intent: `Register(ctx, client)`, `Unregister(client)`, and `Publish(msg)`. Each one owns the race a
raw channel send would leave to its caller, because the run loop stops reading the moment shutdown
is signalled: `Register` and `Publish` report `false` instead of blocking forever, and `Unregister`
becomes a no-op. Registration additionally gives up when the request context is done, so a client
whose HTTP request went away never joins. Handing out the channels instead would mean every caller
re-deriving all of that.

Reads obey the same rule. `ClientCount()` sends a reply channel down `countReq` and lets the run
loop answer it, rather than reading the map from the caller's goroutine. Because the loop is
sequential, a reply also proves everything queued before the request has already been processed —
which is what makes it a usable synchronization barrier in tests.

**Client** (`client.go`) — one per connection, with two goroutines:

- *read pump* — reads frames, enforces the rate limit, normalizes the payload, and hands it to
  `Hub.Publish`. Exits on any read error and unregisters the client.
- *write pump* — selects over the client's 256-message `send` channel, a 54-second ping ticker, and
  the hub's `stopping()` channel. Coalesces anything already queued into the current frame, separated by
  newlines, so a burst costs one frame rather than one per message. The frame body comes from
  `writeFrame`, a function of a writer and the queue, so that format is unit-tested without a socket.

Splitting reads and writes is required by `gorilla/websocket`: at most one concurrent reader and one
concurrent writer are allowed per connection. How many pumps that takes is the client's business, not
the hub's: the hub starts one goroutine per registered client, running `serve`, which runs the read
pump on that goroutine, the write pump on a second, and returns only once both have exited. The hub's
`WaitGroup` therefore has one entry per connection rather than two, and still covers both pumps.

**Rate limiter** (`rate_limiter.go`) — a token bucket per connection, embedded in the `Client` by
value. Tokens refill continuously from elapsed time rather than on a timer, so there is no background
goroutine and no allocation per client. It carries no mutex because only that connection's read pump
ever touches it; sharing a limiter across goroutines would be a bug.

The bucket reads the clock itself on the path that matters. `newRateLimiter` and `allow()` take no
instant, so the read pump has no time to get wrong and the throttle a connection gets is the one it
was configured with. Behind each sits an `-At` variant — `newRateLimiterAt(…, now)` and `allowAt(now)`
— that takes the instant as an argument. That pair is the test seam, and it is what makes refill
observable: the unit tests advance a fixed instant by hand and pin partial refill, the cap at
capacity, a clock that does not move, and one that goes backwards, none of which can be seen from a
test that has to spend the real time first.

The seam is package-scoped, so the compiler cannot keep production code off it; `TestClockSeamIsTestOnly`
does instead. It parses every non-test file in the package and fails if anything but the two wrappers
names an `-At` variant, which is what turns "production supplies no instant" from a convention into a
checked property. It matches the wrappers by receiver as well as name, and walks whole files rather
than function bodies, so neither an unrelated method called `allow` nor a package-level `var` slips
past. What it does not cover is the struct itself: `rateLimiter`'s fields are package-scoped too, so a
composite literal or a direct write to `last` could still arrange a stale baseline without naming the
seam. That door predates the seam and is unchanged by it.

The instant is an argument on the seam rather than a `func() time.Time` field on the struct
deliberately: a limiter sits by value inside every `Client` and `allow` runs once per message, so a
function-valued field would add an indirect call to the hot path and a word to every connection. The
seam costs neither — `newRateLimiterAt` inlines into its wrapper, `allow` is one static call into
`allowAt`, and the struct is unchanged.

**Origin validation** (`origin.go`) — an `originPolicy`, built once when the hub is constructed:
the allow-list normalized to lowercase `scheme://host` in a lookup set, and whether it contained `*`.
Everything about origins lives in that one type — reading the configured list, `*`, dropping invalid
entries, and the check itself — so its rules are unit-tested over plain strings. The hub's resolved
configuration holds it, and its `checkOrigin` is bound as `CheckOrigin` into the upgrader that the
hub's `/ws` handler builds, so rejection happens before any connection resources are allocated. The check normalizes the
`Origin` header the same way, but headers that are already canonical — which is what browsers send
— match the set directly and skip URL parsing entirely. A request with no `Origin` header is always
rejected, even when the allow-list contains `*`.

**Config** (`config.go`) — parsed from the environment at startup into a `Config`, then resolved once
by `NewHub`: defaults substituted for anything invalid, the allow-list normalized into a lookup set.
The result is a value the hub owns and never mutates, so readers on the connection path need neither
a lock nor an atomic load, and two hubs in one process can be configured differently. The caller's
`Config` is copied on the way in, so changing it afterwards cannot change a running hub. Invalid
values never abort startup; they log and fall back.

**Logging** (`logging.go`) — a single `log/slog` logger shared by the package, its level read from
`LOG_LEVEL`. Per-message records are emitted at debug level behind a `debugEnabled()` check, so at
the default `info` level a busy server skips work that exists only to be logged — slog cannot do
that itself, because arguments are evaluated before it sees them.

## Message flow

```mermaid
sequenceDiagram
    participant A as Client A
    participant RA as A read pump
    participant H as Hub
    participant WB as B write pump
    participant B as Client B

    A->>RA: {"content":"hi"}
    RA->>RA: rate limit check
    RA->>RA: normalize payload
    RA->>H: BroadcastMessage{Sender: A, Payload}
    H->>H: iterate clients, skip sender
    H->>WB: send channel
    WB->>B: text frame
```

Two consequences fall out of this design:

- **The sender never receives its own message.** `BroadcastMessage` carries the sender so the hub can
  skip it. Clients that want a local echo must add it themselves.
- **Payloads are normalized.** Every frame is re-encoded into exactly `{"content":...}`, so unknown
  fields are dropped, clients cannot smuggle extra data through, and a message missing `content` is
  relayed as `{"content":""}`. A frame that already *is* in that canonical form — the common case —
  is detected with a single scan and copied through without a JSON round trip. Anything else falls
  back to decode-and-re-encode. Both paths produce byte-identical output, which is pinned by a
  property test against `encoding/json`.

### Backpressure

Fan-out is a non-blocking send into each client's 256-slot buffer. If the buffer is full, the
client is dropped rather than allowed to stall the hub — one slow consumer cannot block the broadcast
loop for everyone. The trade-off is that a client on a bad network loses its connection instead of
its messages being queued indefinitely.

## Lifecycle

`main` supplies the logger, the config, and a context cancelled on `SIGINT`/`SIGTERM`, then calls
`Run`. It holds no lifecycle logic of its own, so the ordering below is reachable from a test.

**Startup** (`New`, then `Run`): `New` hands the config to the hub, which resolves and keeps it,
builds the mux, and constructs the `http.Server` with 15s read/write and 60s idle timeouts. `Run` starts the hub
goroutine, calls `ListenAndServe` in a goroutine, and blocks on either a listener error or the
context being done. A listener error drains the hub before `Run` returns it, so a failed listen
leaves no goroutines behind. `Serve` shares all of this; it only replaces `ListenAndServe` with
`http.Server.Serve` on the caller's listener, which is how the integration tests run a real service
on an ephemeral port whose origin they know before the service is built.

**Shutdown**, on cancellation, in strict order with a 30-second overall cap. `Run` builds one
`context.Context` carrying that cap and derives a 15-second child for each stage, so a stage that
overruns cannot borrow the other's budget:

1. `http.Server.Shutdown` (15s context) — stops accepting connections and drains in-flight requests.
   Upgraded WebSocket connections are hijacked, so this stage does not wait on them.
2. `Hub.Shutdown` (15s context) — closes the `shutdown` channel, which stops the run loop, closes
   every client connection, and waits on the `WaitGroup` for all pumps to exit. Both of those waits
   share the one 15-second deadline.

The two errors are joined rather than short-circuited, so a hub that overran is still reported when
the HTTP stage failed too.

The `shutdown` channel appears in every blocking select in the codebase — inside `Register`,
`Unregister`, and `Publish`, and in the write pump's own event loop — so nothing can block shutdown
by waiting on a channel nobody will read.

## Concurrency model

| Goroutine        | Count            | Lifetime                       | Started by                |
| ---------------- | ---------------- | ------------------------------ | ------------------------- |
| Hub run loop     | 1                | Process lifetime               | `Hub.Start`               |
| Client read pump | 1 per connection | Until read error or shutdown   | Hub run loop, via `serve` |
| Client write pump| 1 per connection | Until send closed or shutdown  | `Client.serve`            |
| `ListenAndServe` | 1                | Process lifetime               | `Service.Run`             |

Roughly two goroutines and a 256-message buffer per connection. Only the first of the two is the
hub's to launch and to wait on; the second belongs to the client, which is why `serve` does not
return until it has stopped. The whole suite runs under `-race` in CI because this is where the risk
lives.

## Performance

The hot paths are the ones that run per message, and all three are allocation-free or close to it.
Measured with `make bench` on an AMD Ryzen 7 7800X3D; treat the absolute numbers as machine-specific
and the allocation counts as the durable part.

| Path                                | Cost                    | Notes                                        |
| ----------------------------------- | ----------------------- | -------------------------------------------- |
| Broadcast fan-out, 1000 clients     | ~35 us, 0 allocs        | Includes draining all 999 receivers; see below |
| Message normalization, canonical    | ~46 ns, 1 alloc         | Scan and copy; no JSON round trip             |
| Message normalization, slow path    | ~590 ns, 8 allocs       | `encoding/json` decode plus hand-rolled encode |
| Rate limit check                    | ~6 ns, 0 allocs         | Lock-free token bucket                        |
| Origin check, canonical header      | ~23 ns, 0 allocs        | Per handshake, not per message                 |

The fan-out figure is conservative: `BenchmarkHubBroadcast` drains every receiver inside the timed
loop, because a benchmark outruns any consumer goroutine and the hub would otherwise start evicting
clients mid-measurement. Real fan-out is cheaper than the number suggests.

The single remaining allocation per message is the normalized payload itself, which is shared by
reference with every receiving client rather than copied per recipient. Write buffers are pooled
across connections by `gorilla/websocket`, so memory per idle connection stays close to the 256-slot
send channel.

Two decisions do more for throughput than any of the micro-optimizations above: per-message logging
only happens at debug level, and the write pump coalesces everything already queued into a single
frame.

## Design trade-offs

**Nothing configurable is global.** The hub belongs to a `Service` and the configuration belongs to
the hub, so a process can run as many services as it likes with different origin rules and different
limits — which is what lets every test have a real server of its own and run alongside the others.
What is still process-wide is deliberately not configurable per hub: the logger, and the
`sync.Pool` of write buffers that keeps memory flat as connections accumulate. The cost is that a
hub's settings are fixed once it is built; changing them means building another one.

**No message history, no persistence.** Nothing is stored, so there is no database, no migration
path, and no state to back up. A client that reconnects starts from silence.

**Single instance only.** The hub lives in process memory, so two instances behind a load balancer
form two disconnected chat rooms. Horizontal scaling would require an external bus (Redis pub/sub,
NATS) between the hubs.

**Drop rather than queue.** Both the rate limiter (drops messages) and the send buffer (drops
clients) fail fast instead of buffering. This bounds memory under load but makes message delivery
best-effort, with no acknowledgement in the protocol.

**Structured logging without request IDs.** `log/slog` gives levels and key-value attributes, but
there is no correlation ID tying a client's frames together across records. Adding one would mean
threading a connection ID through the pumps.

## Related

- [API reference](../reference/api.md) — the externally visible contract
- [Configuration reference](../reference/configuration.md) — every tunable and every constant
- [Testing](../guides/testing.md) — how the concurrency guarantees are exercised
- [Contributing](../../CONTRIBUTING.md) — working in this codebase
