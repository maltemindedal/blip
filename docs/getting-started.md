# Getting Started

Build Blip, run it, and watch two browser tabs chat with each other. About 10 minutes.

## Prerequisites

- **Go 1.26.8 or later** — check with `go version`. An older toolchain works too; Go downloads the
  version named in `go.mod` automatically.
- **Git**
- **GNU Make** (optional — every step below also shows the plain `go` command)
  - Windows: `choco install make`
  - macOS: `xcode-select --install`
  - Linux: `apt install make` or `yum install make`

## 1. Clone the repository

```bash
git clone https://github.com/maltemindedal/blip.git
cd blip
```

## 2. Build the server

```bash
make build
```

Without Make:

```bash
go build -o bin/blip ./cmd/server        # macOS / Linux
go build -o bin/blip.exe ./cmd/server    # Windows
```

The binary lands in `bin/`. `make build` runs `go fmt` and `go vet` first, so a formatting or vet
failure stops the build.

## 3. Run it

```bash
./bin/blip          # macOS / Linux
.\bin\blip.exe      # Windows PowerShell
```

Or `make run`, which rebuilds first.

Expected output:

```
time=2026-07-23T15:14:04.081+02:00 level=INFO msg="starting Blip server" version=dev commit=unknown build_time=unknown
time=2026-07-23T15:14:04.081+02:00 level=INFO msg="hub started and ready to manage WebSocket connections"
time=2026-07-23T15:14:04.082+02:00 level=INFO msg="server listening" addr=:8080
```

Logs go to stderr as `log/slog` key-value records. Set `LOG_LEVEL=debug` to see every message the
server relays.

The server is now on <http://localhost:8080>. Confirm it in another terminal:

```bash
curl http://localhost:8080/
# Blip server is running!
```

## 4. Chat with yourself

1. Open <http://localhost:8080/test> in a browser tab.
2. Click **Connect**. The status bar turns green.
3. Open the same URL in a second tab and click **Connect** there too.
4. Type a message in the first tab and press Enter.

The message appears in the second tab. It does **not** appear as an incoming message in the first —
the server broadcasts to everyone *except* the sender, so your own text only shows as the local
"You:" echo the page adds.

The server logs each connection as structured `log/slog` records:

```
time=2026-07-24T15:14:05.101+02:00 level=INFO msg="client registered" addr=127.0.0.1:54321 total_clients=1
time=2026-07-24T15:14:07.842+02:00 level=INFO msg="client registered" addr=127.0.0.1:54322 total_clients=2
```

Individual messages and broadcasts are logged at `debug`, not `info`, so they stay out of the way
until you ask for them. Restart with `LOG_LEVEL=debug` to see them:

```
time=2026-07-24T15:14:09.233+02:00 level=DEBUG msg="received message" addr=127.0.0.1:54321 payload="{\"content\":\"hello\"}"
time=2026-07-24T15:14:09.233+02:00 level=DEBUG msg="broadcast complete" delivered=1 dropped=0
```

## 5. Stop it

Press `Ctrl+C`. The server stops accepting connections, then drains the hub:

```
time=2026-07-24T15:14:31.007+02:00 level=INFO msg="shutdown signal received; draining connections"
time=2026-07-24T15:14:31.007+02:00 level=INFO msg="shutting down HTTP server"
time=2026-07-24T15:14:31.014+02:00 level=INFO msg="HTTP server shutdown completed"
time=2026-07-24T15:14:31.014+02:00 level=INFO msg="initiating hub shutdown"
time=2026-07-24T15:14:31.015+02:00 level=INFO msg="closed client connections" count=2
time=2026-07-24T15:14:31.016+02:00 level=INFO msg="hub shutdown completed"
time=2026-07-24T15:14:31.016+02:00 level=INFO msg="server stopped gracefully"
```

## What you just ran

Three endpoints, no database, no authentication, no message history:

- `GET /` — health check, and the catch-all for any unmatched path
- `GET /ws` — the WebSocket endpoint clients connect to
- `GET /test` — the page you just used

Defaults: port `:8080`, only `http://localhost:8080` accepted as an origin, 512-byte messages, 5
messages/second per connection. All five settings are environment variables — see the
[configuration reference](reference/configuration.md).

## Troubleshooting

**`address already in use` / `bind: Only one usage of each socket address`**
Something else holds port 8080. Find it with `lsof -i :8080` (macOS/Linux) or
`netstat -ano | findstr :8080` (Windows), or move Blip: `SERVER_PORT=:9090 ./bin/blip`.

**The test page says "Connection error" and the log says `blocked WebSocket connection from disallowed origin`**
You reached the page at a hostname other than `localhost:8080` — `127.0.0.1:8080` counts as a
different origin. Either use `http://localhost:8080/test` or allow the origin you are using:

```bash
ALLOWED_ORIGINS=http://localhost:8080,http://127.0.0.1:8080 ./bin/blip
```

**Messages stop arriving but the connection stays open**
You are hitting the rate limit — more than 5 messages per second. Excess messages are dropped
silently. Raise `RATE_LIMIT_BURST` if that is too strict.

**`permission denied` on macOS/Linux**
`chmod +x bin/blip`.

## Next steps

- [Connecting a client](guides/connecting-a-client.md) — talk to the server from your own code
- [Configuration reference](reference/configuration.md) — every environment variable
- [API reference](reference/api.md) — endpoints, message format, failure modes
- [Architecture overview](architecture/overview.md) — how the hub and client pumps fit together
- [Deploying to production](guides/deploying-to-production.md) — TLS, reverse proxy, systemd
