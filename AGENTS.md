# AGENTS.md

Blip is a single-binary WebSocket relay: a message sent to `/ws` reaches every other connected client. A change here usually has to protect race-free hub shutdown, the allocation-free per-message hot path, wire output byte-identical to `encoding/json`, and docs that mirror the code.

## Commands

Pre-PR gate, mirroring CI's `test`, `lint`, `vulncheck` and `bench` jobs. Lint with the golangci-lint that `make install-tools` builds into `$(go env GOPATH)/bin` (CI's pinned version, compiled by your Go): one built with an older Go, such as a preinstalled binary, refuses this module.

```bash
go mod verify
go mod tidy && git diff --exit-code go.mod go.sum
go build ./...
go test -race -shuffle=on ./...
golangci-lint run --timeout=10m
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
go test -run '^$' -bench . -benchtime=10x ./internal/...
```

- Edit loop: `go test -race ./...`. `make test` adds `-v` and buries the result in about 1,500 lines of log output.
- Single test: `go test -race -run '^TestHubShutdownIsIdempotent$' -v ./internal/server` (unit tests; integration tests are in `./test/integration`).

CI fails on these and on the image build (`docker build -t blip:dev .` locally, after editing `Dockerfile` or `.dockerignore`). Trivy findings, the Codecov upload (no token is configured) and coverage never fail it. Where CONTRIBUTING.md suggests `make ci-local`, run the gate above. Run `make clean`, `make all`, `make ci-local` or `make release` only when the user asks for that target: each calls `go clean -cache -testcache -modcache`, which wipes the build and module caches of every Go project on the machine.

The lifecycle tests in `test/integration` listen on fixed ports 127.0.0.1:18082-18086, so two test runs on one host fail with `bind: address already in use`. With parallel worktrees, run the suite one at a time and treat that error as a concurrent run: wait and rerun. A new fixed-port test needs a port no other test uses.

## Rules and what enforces them

A failing check names the file to edit. When an agent breaks a rule that nothing enforces twice, add a check and update its row.

| Rule | Enforced by |
| --- | --- |
| `handleBroadcast`, `rateLimiter.allow` and `originPolicy.checkOrigin` make 0 allocations; `normalizeMessage` makes 1 for a canonical message | `TestHubBroadcastDoesNotAllocate`, `TestRateLimiterAllowDoesNotAllocate`, `TestOriginCheckDoesNotAllocate`, `TestNormalizeMessageAllocatesOnlyThePayload`. The benchmarks assert nothing |
| Only the hub's run loop touches the client map | The compiler: the map is in a `registry` local to `Hub.run` |
| Code past `New` never names the raw `Config`; every `Config` field reaches the settings | `TestRawConfigStopsAtTheBoundary`, `TestEveryConfigFieldReachesTheSettings` |
| Per-message logs go at Debug inside `if debugEnabled()` | Partly: the allocation tests fail on such a log in those four functions only if it allocates. Otherwise judgment (Logging, below) |
| A log `msg` is a constant string, and docs and the alert table in `docs/guides/deploying-to-production.md` quote it exactly | sloglint `static-msg` in `.golangci.yml`; `TestAlertTableMatchesLogCalls`, `TestDocsQuoteLogMsgsExactly` |
| Unit tests get a hub from `startTestHub` and a service from `newTestService` | `TestMain` in `hub_internal_test.go` fails the binary if a goroutine of the package outlives the tests |
| `x_internal_test.go` sits beside `x.go`; a test helper has a caller | `TestInternalTestFilesNameASourceFile` (the name only); lint `unused`, since helpers are unexported in the package that uses them |
| Production code reads the clock itself, never through the `allowAt` seam | `TestClockSeamIsTestOnly` |
| A guard test (its comment says it `pins`, `guards` or `enforces`) fails under the bug it guards | `TestEveryGuardTestHasAMutant` and `TestGuardTestsKillTheirMutants` in `internal/mutation`, without `-race`; see `docs/guides/testing.md` |
| Docs mirror code: the Go version in `go.mod`, tool pins in `ci.yml`, each env var the code reads (configuration reference, `.env.example`, `docker-compose.yml`, the test file beside its reader), named tests and helpers, Makefile targets | `TestGoVersionAgreesWithGoMod` and CI's `docker` job (`GOTOOLCHAIN=local`); `TestToolPinsAgreeWithCI`; `TestEnvVarsAreMirrored`; `TestDocIdentifiersExist`; `TestMakefileDocumentedTargetsHaveRules`, `TestMakefileDryRunsEveryDocumentedTarget` (skipped without `make`) |
| `go.mod` and `go.sum` are tidy | CI's `test` job |
| The only module dependency is `github.com/gorilla/websocket`, test-only and `golang.org/x/*` modules included, as the README states | Nothing: judgment |
| The constants table in `docs/reference/configuration.md` and the performance table in `docs/architecture/overview.md` match the code; the rules below | Nothing: judgment |

## Judgment rules

- **Hub ownership.** Before changing `hub.go`, `client.go`, `lifecycle.go` or `service.go`, read `docs/architecture/overview.md`. Every send to the run loop (`register`, `unregister`, `publish`) and the write pump's select carry a `<-quit` case (the write pump reads it as `<-hub.stopping()`); any other blocking wait needs a bound of its own, as the overview's lifecycle section lists.
- **Logging.** slog builds its arguments before it checks the level, so a per-message log outside `if debugEnabled()` costs every message. A WARN a client can trigger repeatedly logs once per episode, as the rate-limit warning does (`TestRateLimitWarnsOncePerEpisode` covers that one only). Renaming a log `msg` means updating the alert table.
- **Scope.** Add an env var, `Config` field or log line only when the task asks for one, since each becomes documented contract. Report problems outside the task instead of fixing them. Remove helpers your own change leaves unused.
- **Claims.** Base every statement in a commit message, PR body, comment or doc on a command you ran or code you read, and say "not verified" otherwise. Overstated claims are this repo's most repeated correction; Docker and CI behaviour are where they have slipped through.
- **Commits and PRs.** Conventional type, optional scope, capitalized imperative subject (`fix(security): Log a rate-limited client once per episode, not once per frame`); the body says why. One commit per item, each green on its own. Review fixes land as new commits (`fix: Address code review findings`), not amends. A bug fix carries a test that fails before the fix, and its body opens with `BUG FIX` and names the commit that introduced the bug when known. A PR body follows `.github/pull_request_template.md` and lists the exact commands you ran with their results.

## Gotchas

- A handshake without an `Origin` header gets 403 even when `ALLOWED_ORIGINS` is `*`; non-browser clients have to set it. The write pump coalesces queued messages into one frame separated by `\n`, so a reader splits before parsing JSON.
- On an arm64 host, `docker build` yields an arm64 image holding an x86-64 binary: the Dockerfile's `ARG TARGETARCH=amd64` default overrides BuildKit's value. CI on amd64 is unaffected, and the docs that promise per-platform images are wrong (PR #17, N5).
- A red "Code scanning AI findings" check on a PR comes from GitHub's Copilot agent, not from this repo; nothing in the PR can fix it.

## Agent skills

`.agents/skills/` holds only skills written for this repo; generic skills are installed globally. `verify-blip` launches its own server on a free loopback port, relays a message between two real WebSocket clients, checks Origin refusal, `/` and `/test`, and runs the race tests and lint. Run `.agents/skills/verify-blip/scripts/verify.sh all` and read its exit status. Evidence goes to `/tmp/blip-verify/<run>/`.

## Cursor Cloud specific instructions

- `.cursor/environment.json` installs the pinned toolchain and links it into `/usr/local/bin`, reading `go` from `go.mod` and golangci-lint and govulncheck from `.github/workflows/ci.yml`. To bump a tool, change only those files; never hard-code a version in `environment.json`.
- The gate is `.agents/skills/verify-blip/scripts/verify.sh all`: race tests, lint, govulncheck, then a live server relay. Done means its last line is `RESULT: PASS (all)`.

## Docs

- Before writing, moving or renaming a test, read `docs/guides/testing.md`.
- Before changing anything a client observes (message format, close behaviour, limits), read `docs/reference/api.md`.
- Before touching origin checks, size or rate limits, or the `/test` page, read `docs/guides/security-hardening.md` and `SECURITY.md`.
- Before dependency, tooling, CI or Docker work, or before fixing a known problem outside your task, read section 7 ("Needs decision") of PR #17 (`gh pr view 17 -R maltemindedal/blip`): the owner's backlog of pending decisions.
