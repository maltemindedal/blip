# Testing

How the suite is organized and how to run, extend, and measure it.

## Layout

Almost all tests live under `test/`, outside the packages they exercise, so they see
`internal/server` through its exported API only.

```
test/
├── unit/                    # package unit — components in isolation
│   ├── error_handling_test.go   # read/write error paths, registration accounting
│   ├── handlers_test.go         # health handler, routing
│   ├── hub_test.go              # publish, client count, shutdown lifecycle
│   └── websocket_test.go        # upgrader config, method and header validation
├── integration/             # package integration — real servers over real sockets
│   ├── setup_test.go            # shared plumbing: test servers, dialing, assertions
│   ├── multiclient_test.go      # many clients exchanging messages concurrently
│   ├── security_test.go         # origin validation, size limits, rate limiting
│   ├── server_test.go           # health endpoint, full startup path
│   ├── shutdown_test.go         # graceful shutdown, ordering, clients closed
│   └── websocket_test.go        # connection lifecycle, broadcasting
└── testhelpers/             # shared helpers (no tests of its own)
    └── helpers.go
```

Integration tests spin up `httptest` servers and dial them with a real `gorilla/websocket` client,
so they exercise the actual handshake, origin check, and pumps. The lifecycle tests go further and
run the real `server.Service` on a real port, driven the way `main` drives it.

The exception is `internal/server/*_internal_test.go`, which covers what the exported API cannot
reach: the hot paths the benchmarks measure, the `*http.Server` that `New` builds — its address,
its timeouts, and its header limit are the service's own, not a caller's — and anything that needs a
client. `Hub.Register` and `Hub.Unregister` take `clientConn`, the hub's own view of a client, which
only this package can name. `Hub.Publish` loses the same shutdown race and is tested from
`test/unit`, where it belongs, because it needs no client at all.

A fourth kind lives there too: checks on the package's own shape rather than on behaviour anyone
outside can call. `TestResolveConfigPreservesEveryField` compares a resolved `Config` field for field
so a field added to the struct cannot be dropped in resolution unnoticed, and
`rate_limiter_internal_test.go` holds `TestClockSeamIsTestOnly`, which parses the package's non-test
sources to keep production off the rate limiter's clock seam. Neither is reachable from outside the
package by construction — one names an unexported type, the other reads the package's own files.

That view is also what makes the hub's delivery rules testable at all. `hub_internal_test.go` defines
a `fakeClient` — an inbox and an address, no socket underneath — and registers it through the real
`Hub.Register`, so the fan-out, sender exclusion, the backpressure drop, and no-op unregistration are
pinned against the running event loop rather than a copy of it. The backpressure test gives its
victim a one-slot inbox and publishes twice; over a real connection, filling 256 slots faster than a
consumer drains them is not something a test can arrange. Assertions read an inbox with a
non-blocking drain that also reports whether the hub closed it, so a regression fails the test
instead of hanging on a channel nobody will feed.

## Running

```bash
make test                  # everything, with -race and -v
make test-unit             # ./test/unit/... only
make test-integration      # ./test/integration/... only
make race                  # -race without -v
```

Plain Go:

```bash
go test ./...                                   # everything
go test -v -race ./test/unit/...                # one package
go test -v -race -run TestHubShutdown ./test/unit  # one test
```

The integration suite takes roughly 1.5 seconds and the unit suite under a second, because both run
their tests in parallel — each owns its hub, so there is no process state to serialize them. What is
left is a handful of tests that wait on real timeouts, such as `TestWebSocketRateLimiting` waiting out
a refill over a real socket. Always keep `-race` on — the hub and the client pumps are concurrent, and
the suite now runs concurrently too.

## Coverage

```bash
make test-coverage               # coverage.out + coverage.html + per-function table
make test-coverage-unit          # unit-coverage.*
make test-coverage-integration   # integration-coverage.*
```

These pass `-coverpkg=./cmd/...,./internal/...` so coverage is attributed to the code under test
rather than to the test packages, and print `go tool cover -func` at the end. Open `coverage.html`
in a browser for the annotated source.

`make test-coverage` measured **70.4% of statements** on 2026-07-24 (unit 56.6%, integration 61.1%).
That figure spans `./cmd/...` and `./internal/...` together, and `cmd/server` has no tests of its
own, so `internal/server` alone measures higher — 76.3% with `-coverpkg=./internal/...`. The same
number appears in the [README](../../README.md#status); update both together. CI collects coverage
and uploads it to Codecov but does not enforce a threshold — nothing fails a build for dropping
coverage.

## Helpers

`test/testhelpers` provides the shared plumbing. Use it instead of hand-rolling servers and dials:

| Helper                                            | Purpose                                                             |
| ------------------------------------------------- | ------------------------------------------------------------------- |
| `CreateTestServer(t, build)`                      | `httptest` server whose handler is built from its own base URL, closed when the test ends |
| `WaitFor(t, timeout, what, cond)`                 | Poll a condition to a deadline — use instead of `time.Sleep`         |
| `WaitForServer(t, url, timeout)`                  | Block until a just-started server accepts requests                   |
| `Dial(t, wsURL, origin)`                          | Dial a `ws://` URL from a given `Origin`, closed when the test ends  |
| `DialPair(t, wsURL, origin)`                      | The sender/receiver pair delivery tests need                         |
| `ConnectWebSocket(url)`                           | Dial with the default dev origin; returns an error instead of failing |
| `SendMessage(conn, content)`                      | Send `{"content": ...}`                                              |
| `MakeRequest(t, method, url)`                     | HTTP request, fully read; returns a `Response` with the body closed  |
| `AssertStatusCode` / `AssertContentType` / `AssertBody` | Common assertions over a `Response`                            |

`CreateTestServer` takes a builder rather than a handler because the hub owns its configuration and
usually has to allow the server's own origin — which means the config, and therefore the hub, has to
exist before the handler does. The listener is opened first and its URL passed to `build`.

The `integration` package layers its own helpers on top in `setup_test.go` — `newTestServer` (a
server backed by a hub of its own, on the default settings), `newConfiguredTestServer` (the same,
with a callback that varies the config first), `startService` (the real `server.Service`, running on
a real port, stopped by cancelling its context), `dial` / `dialPair` / `dialClients` (which return
only once the hub has registered every connection), and `waitForUnregister`. Prefer those inside that
package: they make client-count assertions exact.

## Writing tests

Follow the conventions already in the suite:

- Name tests `TestSubjectBehavior` — `TestWebSocketOriginValidation`, `TestHubShutdownTimeout`.
- Put anything that needs a listening socket in `test/integration`, everything else in `test/unit`.
- Use table-driven subtests with `t.Run` for multiple scenarios of one behavior.
- Cover the failure path, not just the happy one — most bugs in this codebase live in error handling
  and shutdown ordering.
- Own your state, then run in parallel. Nothing configurable is process-wide: give a test its own hub
  with `server.NewHub(cfg)` and `server.SetupRoutesWithHub`, or a whole service of its own with the
  integration package's `startService`, so both the client counts and the settings it observes belong
  to it alone. A test that does that should call `t.Parallel()`. The one thing still shared by the
  process is the logger, so `TestShutdownStopsAcceptingBeforeDrainingClients` — which reads the
  shutdown ordering off `server.SetLogger` — stays serial. Fixed listen ports are fine in parallel as
  long as no two tests pick the same one.
- Prefer waiting on a channel or polling with a deadline over `time.Sleep` for synchronization. The
  hub's `ClientCount()` is answered by its own event loop, so a reply proves every registration,
  unregistration, and broadcast queued before it has been processed — that is the barrier to wait on,
  via `testhelpers.WaitFor`. A `time.Sleep` is only acceptable when elapsed wall-clock time is the
  behavior under test and there is no clock to drive by hand; say so in a comment. Prefer giving
  yourself one, the way the rate limiter does: `allow()` reads the clock so production has nothing to
  get wrong, and an `allowAt(now)` beside it takes the instant for the tests. The refill rules are
  pinned by unit tests that drive `allowAt` from a fixed instant; the one surviving sleep, in
  `TestWebSocketRateLimiting`, is there because reaching the limiter through a real socket goes
  through `NewClient` and gets the real clock. A clock a caller can pass is a limit a caller can
  loosen, so keep production off a seam like that — and since Go has no visibility level that says
  "tests only", assert it: `TestClockSeamIsTestOnly` parses the package's non-test files and fails if
  anything but the wrapper names the seam. Say in the test what such a check does *not* cover; that
  one guards the seam functions, not every route to a stale baseline.

### Benchmarks

`make bench` runs `go test -bench=. -benchmem ./...`. The benchmarks live alongside the code they
measure, in `internal/server/*_internal_test.go`, because they exercise unexported hot paths:
broadcast fan-out, message normalization, the rate limiter, and origin checks.

`BenchmarkHubBroadcast` builds its client set with `newBenchHub`, which installs fakes through the
hub's own registration path rather than writing the client map behind its back, so the fan-out it
times runs over a map the hub assembled itself. It deliberately leaves the event loop unstarted and
calls the fan-out directly: that keeps the client map owned by the one goroutine touching it and
keeps channel handoff and pump scheduling out of the number.

They assert allocation counts implicitly rather than wall-clock time — the numbers move with the
machine, but a path that was allocation-free and stops being so is a regression worth catching. Run
`go test -bench . -benchmem ./internal/...` before and after a change to the hot path and compare
the `allocs/op` column.

## In CI

The `test` job runs `go test -race -shuffle=on -coverprofile=coverage.out -covermode=atomic ./...`
on Ubuntu with the toolchain from `go.mod`. `-shuffle=on` randomizes test order, so a suite that
depends on ordering fails in CI even when it passes locally. A separate `bench` job runs every
benchmark once (`-benchtime=10x`) to keep them compiling. See
[Contributing](../contributing.md#continuous-integration).

## Related

- [Contributing](../contributing.md) — the full pre-push check list
- [Make targets reference](../reference/make-targets.md) — every test target
- [Architecture overview](../architecture/overview.md) — what the concurrency tests are protecting
