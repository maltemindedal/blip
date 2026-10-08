# Blip

A small WebSocket broadcast server in Go — one binary, one dependency, no database.

[![CI Pipeline](https://github.com/maltemindedal/blip/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/maltemindedal/blip/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/github/go-mod/go-version/maltemindedal/blip)](https://golang.org/)

Clients connect to `/ws`, send JSON messages, and every other connected client receives them. That
is the whole feature set: no rooms, no history, no accounts. What it does bring is the operational
surface you would otherwise write yourself — origin validation against CSWSH, per-connection rate
limiting, message size caps, graceful shutdown, and a static cross-platform binary. Use it as a
real-time relay behind your own application, or as a starting point to build on.

## Quick start

Requires **Go 1.26.8 or later** (`go version`) and Git. GNU Make is optional.

```bash
git clone https://github.com/maltemindedal/blip.git
cd blip

make build            # or: go build -o bin/blip ./cmd/server
./bin/blip          # Windows: .\bin\blip.exe
```

```
time=2026-07-23T15:51:57.081+02:00 level=INFO msg="starting Blip server" version=dev commit=unknown build_time=unknown
time=2026-07-23T15:51:57.081+02:00 level=INFO msg="hub started and ready to manage WebSocket connections"
time=2026-07-23T15:51:57.082+02:00 level=INFO msg="server listening" addr=:8080
```

Logs are structured key-value records via `log/slog`; set `LOG_LEVEL=debug` to trace individual
messages.

Open <http://localhost:8080/test> in two browser tabs, click **Connect** in each, and type. Messages
go to the other tab — senders do not receive their own messages.

With Docker instead:

```bash
docker compose up -d --build
```

## Usage

Send and receive JSON with a single `content` field. The `Origin` header must match the server's
allow-list — browsers set it automatically, other clients must not forget it.

```javascript
const ws = new WebSocket("ws://localhost:8080/ws");

ws.onopen = () => ws.send(JSON.stringify({ content: "Hello" }));
ws.onmessage = (event) => console.log(JSON.parse(event.data).content);
```

Configuration is entirely environment variables:

```bash
SERVER_PORT=:8080 \
ALLOWED_ORIGINS=http://localhost:8080,http://localhost:3000 \
MAX_MESSAGE_SIZE=512 \
RATE_LIMIT_BURST=5 \
LOG_LEVEL=info \
./bin/blip
```

## Documentation

Full index: [docs/README.md](docs/README.md).

| Guide                                                             | Contents                                     |
| ----------------------------------------------------------------- | -------------------------------------------- |
| [Getting started](docs/getting-started.md)                        | Build, run, and try it in ~10 minutes         |
| [Connecting a client](docs/guides/connecting-a-client.md)         | JavaScript, Go, Python, websocat examples     |
| [Configuration](docs/reference/configuration.md)                  | Every environment variable and default        |
| [API reference](docs/reference/api.md)                            | Endpoints, message protocol, failure modes    |
| [Deploying to production](docs/guides/deploying-to-production.md) | TLS, reverse proxy, systemd, scaling limits   |
| [Security hardening](docs/guides/security-hardening.md)           | What is protected, and what is not            |
| [Architecture overview](docs/architecture/overview.md)            | Hub, client pumps, design trade-offs          |

## Endpoints

| Endpoint | Purpose                                                          |
| -------- | ---------------------------------------------------------------- |
| `/ws`    | WebSocket endpoint. `GET` only; rejects disallowed origins with 403 |
| `/`      | Health check returning `Blip server is running!` (also the catch-all for unmatched paths) |
| `/test`  | Built-in browser test page — development only                     |

## Project structure

```
cmd/server/         Entry point: logger, config, signal handling
internal/server/    Service lifecycle, hub, client pumps, handlers, config, origin checks, rate limiter
test/               Integration suite plus shared helpers (unit tests sit beside the code)
docs/               Documentation (see docs/README.md)
.github/workflows/  CI pipeline
```

## Status

Actively maintained, single-instance by design. The hub lives in process memory, so multiple
instances behind a load balancer form separate chat rooms — see
[scaling](docs/guides/deploying-to-production.md#scaling). There is no built-in authentication;
enforce it in front of `/ws` if your data needs it.

Test coverage was 83.9% of statements as of 2026-10-08 (`make test-coverage`). Broadcast fan-out and
rate limiting are allocation-free per message, and the origin check is allocation-free per
handshake — see
[performance](docs/architecture/overview.md#performance) for the numbers and `make bench` to
reproduce them.

## Contributing

Bug reports, features, and pull requests are welcome — see
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT — see [LICENSE](LICENSE).
