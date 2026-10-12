# Blip verification map

The maintained source for verifying Blip's user-facing behavior. Read this index first.

## Baseline preconditions

- Go satisfying `go.mod`, `curl`, `python3` (free-port pick), and for `lint` CI's golangci-lint in `$(go env GOPATH)/bin`.
- `verify.sh doctor` exits 0.
- Drive only a server this run launched.

## Driving conventions

- Run every step through `.agents/skills/verify-blip/scripts/verify.sh`.
- Non-browser clients must send an allowed `Origin`. `wsclient -origin` does.

## Proof and skip reporting

- Proof is the frames each client observed plus the server log. Report a SKIP as unverified.

## Feature entry contract

Each feature file has an H1, a paragraph, then `Sub-features`, `How to get to it (user POV)`, `Driving it with verify.sh`, `Gotchas`.

## Features

- [Message relay](./relay.md) covers the broadcast to other clients, normalization, and sender exclusion.
- [Origin check](./origin-check.md) covers refusing handshakes from disallowed or missing origins.
- [Health and test page](./health-and-test-page.md) covers `/` and `/test`.
- [Rate and size limits](./limits.md) is mapped but not yet automated.
