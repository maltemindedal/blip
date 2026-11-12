# Testing

How the suite is organized and how to run, extend, and measure it.

## Layout

Tests live in two places, split by what they reach: the package's unexported code, or the running
service as a client sees it.

```
internal/server/
└── *_internal_test.go       # package server — unit tests and benchmarks, one file per source file
internal/mutation/           # test-only — past mistakes as mutants the guard tests must fail
test/
├── integration/             # package integration — a real Service over real sockets
│   ├── setup_test.go            # shared plumbing: test services, registered dials
│   ├── helpers_test.go          # shared plumbing: polling, HTTP requests, raw dials, assertions
│   ├── error_handling_test.go   # read/write error paths, registration accounting
│   ├── multiclient_test.go      # many clients exchanging messages concurrently
│   ├── security_test.go         # origin validation, size limits, rate limiting
│   ├── server_test.go           # health endpoint, full startup path
│   ├── shutdown_test.go         # graceful shutdown, ordering, clients closed
│   └── websocket_test.go        # connection lifecycle, broadcasting
└── repocheck/               # test-only — docs, pins and the Makefile cross-checked against the code
```

**Unit tests** are package-internal: `x_internal_test.go` covers `x.go` and can reach the package's
unexported code. The hub's lifecycle and fan-out, the handlers and routes, configuration, the wire
encoder, the rate limiter, the origin policy and the write pump's framing are pinned there, with the
benchmarks for the hot paths. Most of them need no socket: a handler is driven through
`httptest.NewRecorder`, and a hub through `fakeClient`, below. The service's tests are the
exception. They check the `*http.Server` that `New` builds — its address, its timeouts, and its
header limit are the service's own, not a caller's — and what `Run` and `Serve` do when a listener
fails or the context is cancelled, so some of them open a listener of their own.

**Integration tests** run the real `server.Service` on an ephemeral loopback port, through
`Service.Serve`, and dial it with a real `gorilla/websocket` client, so they exercise the actual
handshake, origin check, and pumps. The lifecycle tests go further and start it through `Run` on a
fixed port, driven the way `main` drives it. They see `internal/server` through its exported API
only, which is what keeps them testing the service rather than a copy of it.

Checks on the package's own shape, rather than on behaviour anyone outside can call, are unit tests
too. `TestEveryConfigFieldReachesTheSettings` changes each `Config` field in turn and requires the
resolved settings to change, so a field added to the struct cannot be ignored in resolution
unnoticed; `TestRawConfigStopsAtTheBoundary` parses the package's non-test sources to keep the raw
`Config` from being named past `New`; and
`rate_limiter_internal_test.go` holds `TestClockSeamIsTestOnly`, which parses the package's non-test
sources to keep production off the rate limiter's clock seam. None is reachable from outside the
package by construction: the first names an unexported type, the others read the package's own files.

That view is also what makes the hub's delivery rules testable at all. `hub_internal_test.go` defines
a `fakeClient` — an inbox and an address, no socket underneath — and registers it through the real
`Hub.register`, so the fan-out, sender exclusion, the backpressure drop, and no-op unregistration are
pinned against the running event loop rather than a copy of it. The backpressure test gives its
victim a one-slot inbox and publishes twice; over a real connection, filling 256 slots faster than a
consumer drains them is not something a test can arrange. Assertions read an inbox with a
non-blocking drain that also reports whether the hub closed it, so a regression fails the test
instead of hanging on a channel nobody will feed.

## Running

```bash
make test                  # everything, with -race and -v
make test-unit             # ./internal/... only — the package-internal tests
make test-integration      # ./test/integration/... only
make race                  # -race without -v
```

Plain Go:

```bash
go test ./...                                   # everything
go test -v -race ./internal/server                                        # one package
go test -v -race -run '^TestHubShutdownIsIdempotent$' ./internal/server  # one test
```

Each suite takes a few seconds with `-race` (about 2 for integration and 3 for `internal/server`,
measured 2026-10-08), because both run their tests in parallel — each owns its hub, so there is no process state to serialize them. What is
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

`make test-coverage` measured **83.9% of statements** on 2026-10-08, the same on two runs (unit
70.6% on both; integration 69.9% and 70.6%, as it varies by about a point from run to run). That
figure spans `./cmd/...` and `./internal/...` together, and `cmd/server` has no tests of its own, so
`internal/server` alone measures higher — 86.5% and 86.9% on the same two runs with
`-coverpkg=./internal/...`. The same number appears in the [README](../../README.md#status); update
both together. CI collects coverage and uploads it to Codecov but does not enforce a threshold —
nothing fails a build for dropping coverage.

## Helpers

`test/integration/helpers_test.go` holds the integration suite's shared plumbing. Use it instead of
hand-rolling dials and requests:

| Helper                                            | Purpose                                                             |
| ------------------------------------------------- | ------------------------------------------------------------------- |
| `waitFor(t, timeout, what, cond)`                 | Poll a condition to a deadline — use instead of `time.Sleep`         |
| `waitForServer(t, url, timeout)`                  | Block until a just-started server accepts requests                   |
| `dialUnregistered(t, wsURL, origin)`              | Dial a `ws://` URL from a given `Origin` without waiting for the hub; closed when the test ends |
| `connectWebSocket(url)`                           | Dial with the default dev origin; returns an error instead of failing |
| `sendMessage(conn, content)`                      | Send `{"content": ...}`                                              |
| `makeRequest(t, method, url)`                     | HTTP request, fully read; returns a `response` with the body closed  |
| `assertStatusCode` / `assertContentType` / `assertBody` | Common assertions over a `response`                            |

The helpers are unexported functions in a `_test.go` file of the package that uses them, so the
linter's `unused` check reports one that loses its last caller. An exported helper in a package of
its own counts as used whether or not anything calls it.

`setup_test.go` layers the service helpers on top — `newTestServer` (a
real `server.Service` with a hub of its own, on an ephemeral port and the default settings),
`newConfiguredTestServer` (the same, with a callback that varies the config first — the listener is
opened before the service is built, so its own origin is already on the allow-list), `startService`
(the same service started through `Run` on a fixed port, stopped by cancelling its context), `dial` /
`dialPair` / `dialClients` (which return only once the hub has registered every connection), and
`waitForUnregister`. Prefer those to a raw dial: they make client-count assertions exact.

## Writing tests

Follow the conventions already in the suite:

- Name tests `TestSubjectBehavior` — `TestWebSocketOriginValidation`,
  `TestHubShutdownReturnsPromptlyWhenIdle`.
- Put a test that needs only the exported API, and talks to the running service the way a client
  does, in `test/integration`. Put a test that needs unexported code in `internal/server`, in the
  `_internal_test.go` file named after the file it covers, even when it opens a listener of its own.
- Use table-driven subtests with `t.Run` for multiple scenarios of one behavior.
- Cover the failure path, not just the happy one — most bugs in this codebase live in error handling
  and shutdown ordering.
- Own your state, then run in parallel. Nothing configurable is process-wide: give a unit test its own
  hub with `startTestHub(t, cfg)`, or an integration test a whole service of its own with
  `newConfiguredTestServer`, so both the client counts and the settings it observes belong
  to it alone. A test that does that should call `t.Parallel()`. The one thing still shared by the
  process is the logger, so `TestShutdownStopsAcceptingBeforeDrainingClients` — which reads the
  shutdown ordering off `server.SetLogger` — stays serial. Fixed listen ports are fine in parallel as
  long as no two tests pick the same one.
- Prefer waiting on a channel or polling with a deadline over `time.Sleep` for synchronization. The
  hub's `ClientCount()` is answered by its own event loop, so a reply proves every registration,
  unregistration, and broadcast queued before it has been processed — that is the barrier to wait on,
  via `waitFor`. A `time.Sleep` is only acceptable when elapsed wall-clock time is the
  behavior under test and there is no clock to drive by hand; say so in a comment. Prefer giving
  yourself one, the way the rate limiter does: `allow()` reads the clock so production has nothing to
  get wrong, and an `allowAt(now)` beside it takes the instant for the tests. The refill rules are
  pinned by unit tests that drive `allowAt` from a fixed instant; the one surviving sleep, in
  `TestWebSocketRateLimiting`, is there because reaching the limiter through a real socket goes
  through `newClient` and gets the real clock. A clock a caller can pass is a limit a caller can
  loosen, so keep production off a seam like that — and since Go has no visibility level that says
  "tests only", assert it: `TestClockSeamIsTestOnly` parses the package's non-test files and fails if
  anything but the wrapper names the seam. Say in the test what such a check does *not* cover; that
  one guards the seam functions, not every route to a stale baseline.

### Benchmarks

`make bench` runs `go test -run '^$' -bench=. -benchmem ./...`; `-run '^$'` skips the tests so only
the benchmarks execute. The benchmarks live alongside the code they measure, in
`internal/server/*_internal_test.go`, because they exercise unexported hot paths: broadcast fan-out,
message normalization, the rate limiter, and origin checks.

`BenchmarkHubBroadcast` builds its client set with `newBenchHub`, which installs fakes through the
hub's own registration path rather than writing the client map behind its back, so the fan-out it
times runs over a map the hub assembled itself. It deliberately leaves the event loop unstarted and
calls the fan-out directly: that keeps the client map owned by the one goroutine touching it and
keeps channel handoff and pump scheduling out of the number.

They assert allocation counts implicitly rather than wall-clock time — the numbers move with the
machine, but a path that was allocation-free and stops being so is a regression worth catching. Run
`go test -bench . -benchmem ./internal/...` before and after a change to the hot path and compare
the `allocs/op` column.

## Repository checks

`test/repocheck` holds tests about the repository rather than the service. They cross-check facts
that docs and config copy by hand, and they run with `go test ./...` and in CI's `test` job. Each
failure message names the file to edit and the fix.

| Check                                      | What it compares                                                              |
| ------------------------------------------ | ----------------------------------------------------------------------------- |
| `TestMakefileDocumentedTargetsHaveRules`   | Every `## name:` help comment is followed by its `name:` rule line            |
| `TestMakefileDryRunsEveryDocumentedTarget` | `make -n <target>` succeeds for each documented target (skipped without make) |
| `TestToolPinsAgreeWithCI`                  | golangci-lint and govulncheck versions match `ci.yml`; neither is `@latest`   |
| `TestGoVersionAgreesWithGoMod`             | Go versions named in docs and the Dockerfile match `go.mod`                   |
| `TestDocIdentifiersExist`                  | Test, benchmark and helper names in docs are declared in some `.go` file      |
| `TestAlertTableMatchesLogCalls`            | The alert table's `msg` and level match a `slog` call                         |
| `TestDocsQuoteLogMsgsExactly`              | Log `msg` strings quoted in docs match the code's capitalization              |
| `TestEnvVarsAreMirrored`                   | Each env var the code reads appears in the configuration reference, `.env.example`, `docker-compose.yml` and `config_internal_test.go` |
| `TestInternalTestFilesNameASourceFile`     | `x_test.go` and `x_internal_test.go` sit beside an `x.go`                     |

The package finds the repository root by walking up to `go.mod`, so a compiled test binary checks
whichever checkout it runs in: `go test -c -o /tmp/repocheck.test ./test/repocheck`, then run it from
the root of a `git archive` of an older commit to replay a past mistake.

A new check goes in the file for its subject (`makefile_test.go`, `versions_test.go`,
`docs_test.go`, `envvars_test.go`, `testfiles_test.go`), using the helpers in `repo_test.go`. Give it
a failure message that names the file and the edit. Before merging, break the thing it guards in a
scratch copy and watch the check fail, and say in its comment what it does not cover.

## Guard tests and mutants

A guard test protects an invariant, and several here first passed under the bug they were written to
catch. `internal/mutation` checks that they now fail. Its table, `mutants` in
`internal/mutation/mutants_test.go`, lists past mistakes as edits to production code. For each one the
harness copies the module to a temporary directory, applies the edits, runs only the tests named in
`Kills`. It fails if a named test passes or does not run, if an `Old` text is not found exactly once,
if the mutant does not compile, or if an edit touches a `_test.go` file. `go test ./internal/...` runs it, and `-short`
skips it with a message. It adds a few seconds, because each mutant builds `internal/server` once.

When you add a guard test, add its mutant in the same commit:

1. Write the test, and open the comment above it with the test's name followed by `pins`,
   `guards` or `enforces`.
2. Add a `mutant` that reintroduces the bug the test exists to catch. `Name` says what is wrong, `Origin`
   names the commit that introduced the bug or added the test that kills it, `Edits` are `{File, Old,
   New}` replacements (an empty `Old` adds a new file), and `Kills` lists the test.
3. Run `go test -run TestGuardTestsKillTheirMutants ./internal/mutation`. Break the mutant on purpose
   (change `New` to equal `Old`) and watch the test report `SURVIVED`, then restore it.

`TestEveryGuardTestHasAMutant` fails for a test whose comment says it pins, guards or enforces
something and that no mutant names, except the tests listed in `baseline_test.go`, which predate the
harness. That list may only shrink: when you add a mutant for a listed test, delete its entry. The
check trusts the comment's wording, so a guard test described some other way is not seen.

The harness runs the mutant without `-race`, so a bug only the race detector reveals cannot be a mutant.

## In CI

The `test` job runs `go test -race -shuffle=on -coverprofile=coverage.out -covermode=atomic ./...`
on Ubuntu with the toolchain from `go.mod`. `-shuffle=on` randomizes test order, so a suite that
depends on ordering fails in CI even when it passes locally. A separate `bench` job runs every
benchmark once (`-benchtime=10x`) to keep them compiling. See
[Contributing](../../CONTRIBUTING.md#continuous-integration).

## Related

- [Contributing](../../CONTRIBUTING.md) — the full pre-push check list
- [Make targets reference](../reference/make-targets.md) — every test target
- [Architecture overview](../architecture/overview.md) — what the concurrency tests are protecting
