---
name: verify-blip
description: Drive Blip (single-binary Go WebSocket relay) the way two browser tabs do and capture proof. It runs the race tests and lint, launches its own server on a free loopback port, checks / and /test, and relays a message between two real WebSocket clients, covering Origin refusal, normalization and sender exclusion. Use before claiming a Blip change works.
---

# Verify Blip

Users touch Blip through `/ws` (WebSocket JSON `{"content":...}`), `/` (health) and `/test` (browser page). Everything goes through one helper:

```sh
.agents/skills/verify-blip/scripts/verify.sh <doctor|build|test|lint|launch|health|relay|stop|cleanup|all>
```

Each step prints `PASS`/`FAIL`/`SKIP` lines and ends with `RESULT: PASS`, `RESULT: PASS with SKIP` or `RESULT: FAIL`. The exit code is 0 unless something failed. A SKIP still means that step is unverified.

## Launch

`verify.sh launch` builds `./cmd/server` into the run's work dir. It picks a free loopback port and starts the server with `SERVER_PORT=127.0.0.1:<port>`, `ALLOWED_ORIGINS=http://localhost:<port>` and `LOG_LEVEL=debug`. The server is ready when the log shows `msg="server listening"` and `GET /` returns 200. The PID goes in `server.pid` and the port in `server.port`. Use only this skill's launch. The integration tests own the fixed ports 18082 to 18086, and a random port never collides with them. Teardown is `verify.sh stop` (SIGTERM, which drives graceful shutdown).

## Doctor

`verify.sh doctor` is read-only. It checks that `go env GOVERSION` satisfies `go.mod` (`GOTOOLCHAIN=auto` downloads the right version), that `curl` is present, and that CI's `golangci-lint` is in `$(go env GOPATH)/bin`, where `make install-tools` puts it. It also reports whether this run's server is up.

## Drive

- `test` runs `go test -race -shuffle=on ./...`, CI's test command, and requires exit 0 with no FAIL lines.
- `lint` runs `$(go env GOPATH)/bin/golangci-lint run --timeout=10m`. It SKIPs without that binary. A preinstalled lint built with an older Go refuses this module (AGENTS.md).
- `health` requires `GET /` to return 200 and `GET /test` to return `text/html`.
- `relay` runs `go run ./.agents/skills/verify-blip/wsclient`, a real gorilla/websocket client.
  - A handshake without `Origin` gets 403.
  - Two clients connect with the allowed Origin.
  - The sender sends `{"content":"<hi> & bye","extra":1}`, and the receiver gets exactly `{"content":"\u003chi\u003e \u0026 bye"}`.
  - The sender gets nothing back.
  - The server log shows `client registered ... total_clients=2`.
- `all` runs doctor, test, lint, launch, health, relay and cleanup. It stops after doctor if doctor fails.

Go tooling skips directories that start with `.`, so `wsclient` is not part of `./...`. It adds no dependency and is invisible to build, test, lint, `go mod tidy` and govulncheck.

Feature recipes are in [`features/README.md`](features/README.md).

## Evidence

Artifacts go to `$BLIP_VERIFY_ARTIFACTS`, default `/tmp/blip-verify/<run-id>`. Set `BLIP_VERIFY_RUN=<id>` to keep several steps together. Each step writes `<name>.cmd`, `.stdout`, `.stderr` and `.exit`. The server log is `server.log`, and the response bodies are `health.body` and `testpage.html`. Relay proof is the observed frames plus the hub's log lines, not a unit test.

## Cleanup

`verify.sh cleanup` stops only the PID in this run's `server.pid`, never anything by name. It removes the work dir (the binary) and keeps every artifact.
