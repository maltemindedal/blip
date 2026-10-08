# AGENTS.md

Blip is a single-binary WebSocket relay: a message sent to `/ws` reaches every other connected client. A change here usually has to protect race-free hub shutdown, the allocation-free per-message hot path, wire output byte-identical to `encoding/json`, and docs that mirror the code.

## Commands

Lint with the golangci-lint that `make install-tools` builds into `$(go env GOPATH)/bin` (CI's pinned version, compiled by your Go): one built with an older Go, such as a preinstalled binary, refuses this module.

Pre-PR gate, mirroring CI's `test`, `lint`, `vulncheck` and `bench` jobs:

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
- Allocation check: `go test -run '^$' -bench . -benchmem ./internal/...` (see Conventions).
- After editing `Dockerfile` or `.dockerignore`: `docker build -t blip:dev .` (needs Docker; CI's `docker` job builds the image).
- After editing the `Makefile`: `make -n <target>` for each target near the edit. An agent edit once deleted the `release:` line, and its recipe silently ran inside `build-current`.

CI fails on these and on the image build. Trivy findings, the Codecov upload (no token is configured) and coverage never fail it.

Where CONTRIBUTING.md suggests `make ci-local`, run the gate above. Run `make clean`, `make all`, `make ci-local` or `make release` only when the user asks for that target: each calls `go clean -cache -testcache -modcache`, which wipes the build and module caches of every Go project on the machine.

The lifecycle tests in `test/integration` listen on fixed ports 127.0.0.1:18082-18086, so two test runs on one host fail with `bind: address already in use`. With parallel worktrees, run the suite one at a time and treat that error as a concurrent run: wait and rerun. A new fixed-port test needs a port no other test uses.

## Conventions

- **One dependency.** `go list -m all` prints two lines: this module and `github.com/gorilla/websocket`. Build with the standard library: a new module, test-only or `golang.org/x/*` included, breaks a design constraint the README states, and no CI check catches it. The vendored `.agents/skills/golang-pro` skill suggests zap and `x/time/rate`; this rule wins.
- **Allocation-free hot paths.** Nothing fails when a hot path starts allocating: a per-message allocation added to `handleBroadcast` passes the whole suite and CI's bench job and shows only in the `allocs/op` column. The benchmarks assert nothing, whatever `docs/guides/testing.md` says. When you change `hub.go`, `message_json.go`, `rate_limiter.go` or `origin.go`, run the allocation check on the base commit and on your change, and put both results in the PR. Expected: `BenchmarkHubBroadcast`, `BenchmarkRateLimiterAllow` and `BenchmarkOriginCheck` at 0 allocs/op, `BenchmarkNormalizeMessage` at 1 (the payload itself).
- **Hub ownership.** Before changing `hub.go`, `client.go` or `service.go`, read `docs/architecture/overview.md`. Only the hub's run loop touches the client map, which has no lock: every change arrives through `register`, `unregister` or `publish`. Every send to the run loop (`register`, `unregister`, `publish`) and the write pump's select carry a `<-quit` case (the write pump reads it as `<-hub.stopping()`); any other blocking wait needs a bound of its own, as the overview's lifecycle section lists.
- **Logging.** Per-message logs go at Debug inside `if debugEnabled()`, because slog builds its arguments before it checks the level. A WARN a client can trigger repeatedly logs once per episode, as the rate-limit warning does. Log `msg` strings are an operator contract: renaming one means updating the alert table in `docs/guides/deploying-to-production.md`.
- **Scope.** Add an env var, `Config` field or log line only when the task asks for one, since each becomes documented contract. Report problems outside the task instead of fixing them. Remove helpers your own change leaves unused.
- **Tests.** Before writing, moving or renaming a test, read `docs/guides/testing.md`: whether it belongs in `internal/server` or `test/integration`, the helpers, `t.Parallel()`, and waiting on `Hub.ClientCount()` instead of `time.Sleep`. Name an internal test file after the file it covers (`rate_limiter_internal_test.go`, not `rate_limiter_seam_test.go`).
- **Clock seams.** Production code reads the clock itself; tests drive an `xAt(now)` variant, and a test that parses the package sources (`TestClockSeamIsTestOnly`) keeps production off it. A caller-supplied clock on the production path was tried and reverted: a time a caller chooses is a throttle a caller can loosen.
- **Guard tests.** Mutation-check every test that guards an invariant: break the code, watch the test fail, restore. Several guard tests here first passed under their mutants. Say in the test's comment what it does not cover.
- **Bug fixes** carry a test that fails before the fix. The commit body opens with `BUG FIX` and names the commit that introduced the bug when known.
- **Claims.** Base every statement in a commit message, PR body, comment or doc on a command you ran or code you read, and say "not verified" otherwise. Overstated claims are this repo's most repeated correction; Docker and CI behaviour are where they have slipped through.
- **Docs mirror code.** Update them in the same change:
  - Go version: `go.mod`, `Dockerfile`, `README.md`, `CONTRIBUTING.md` and pages under `docs/` (`git grep` the old version). The builder image runs with `GOTOOLCHAIN=local`, so a stale Dockerfile tag fails the image build.
  - Tool pins: golangci-lint in `.github/workflows/ci.yml`, `Makefile` and `CONTRIBUTING.md`; govulncheck in `ci.yml` and `Makefile`.
  - Env var: `NewConfigFromEnv` in `config.go`, a case in `config_internal_test.go`, `docs/reference/configuration.md`, `.env.example` and `docker-compose.yml`.
  - Compile-time constants: the table in `docs/reference/configuration.md`. A changed `allocs/op`: the performance table in `docs/architecture/overview.md`.
- **Commits.** Conventional type, optional scope, capitalized imperative subject (`fix(security): Log a rate-limited client once per episode, not once per frame`); the body says why. One commit per item, each green on its own. Review fixes land as new commits (`fix: Address code review findings`), not amends.
- **PRs** merge with a merge commit (`gh pr merge --merge`). The body follows `.github/pull_request_template.md` and lists the exact commands you ran with their results.

## Gotchas

- A handshake without an `Origin` header gets 403 even when `ALLOWED_ORIGINS` is `*`; non-browser clients have to set it. The write pump coalesces queued messages into one frame separated by `\n`, so a reader splits before parsing JSON.
- On an arm64 host, `docker build` yields an arm64 image holding an x86-64 binary: the Dockerfile's `ARG TARGETARCH=amd64` default overrides BuildKit's value. CI on amd64 is unaffected, and the docs that promise per-platform images are wrong (PR #17, N5).
- A red "Code scanning AI findings" check on a PR comes from GitHub's Copilot agent, not from this repo; nothing in the PR can fix it.

## Docs

- Before changing anything a client observes (message format, close behaviour, limits), read `docs/reference/api.md`.
- Before touching origin checks, size or rate limits, or the `/test` page, read `docs/guides/security-hardening.md` and `SECURITY.md`.
- Before dependency, tooling, CI or Docker work, or before fixing a known problem outside your task, read section 7 ("Needs decision") of PR #17 (`gh pr view 17 -R maltemindedal/blip`): the owner's backlog of pending decisions.
