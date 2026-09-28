# Configuration Reference

Blip is configured entirely through environment variables, read once at startup. Every variable
except `LOG_LEVEL` is read by `NewConfigFromEnv` (`internal/server/config.go`); `LOG_LEVEL` is read
separately by `LogLevelFromEnv` (`internal/server/logging.go`), because the logger has to exist
before the rest of the configuration can be parsed and warned about. There are no command-line flags
and no configuration file.

An annotated template lives in [`.env.example`](../../.env.example). The process does not read
`.env` itself — use your shell, `docker compose`, or a systemd unit to load it.

The parsed `Config` is resolved once, when the hub is built: defaults are substituted for anything
invalid and the allow-list is normalized into a lookup set. That resolved value belongs to the hub,
so every connection it accepts runs under the settings it was given — nothing is process-wide, and
nothing can be changed while the server is running.

## Variables

| Variable                     | Type                   | Default              | Effect                                                                              |
| ---------------------------- | ---------------------- | -------------------- | ----------------------------------------------------------------------------------- |
| `SERVER_PORT`                | `[host]:port`          | `:8080`              | Listen address passed to `http.Server.Addr`.                                          |
| `ALLOWED_ORIGINS`            | comma-separated list   | `http://localhost:8080` | Origins accepted on the WebSocket handshake. `*` allows every origin.              |
| `MAX_MESSAGE_SIZE`           | integer (bytes)        | `512`                | Read limit per WebSocket message. Exceeding it closes the connection.                 |
| `RATE_LIMIT_BURST`           | integer (messages)     | `5`                  | Token bucket capacity per connection.                                                 |
| `RATE_LIMIT_REFILL_INTERVAL` | integer (whole seconds)| `1`                  | Time to refill the whole bucket. Sub-second values are not expressible.               |
| `LOG_LEVEL`                  | `debug`/`info`/`warn`/`error` | `info`        | Minimum severity written to stderr. `debug` logs every message and broadcast.         |

### Validation behavior

Every variable is optional. Values that fail to parse do not stop the server — the default is
used and a line is written to the log:

```
time=2026-07-23T15:14:04.812+02:00 level=WARN msg="invalid MAX_MESSAGE_SIZE; using default" value=abc default=512
```

Specifics:

- `SERVER_PORT` — a value with no `:` is treated as a bare port and rewritten with a leading colon,
  so `SERVER_PORT=9000` and `SERVER_PORT=:9000` are equivalent. `127.0.0.1:9000` binds to loopback only.
- `MAX_MESSAGE_SIZE`, `RATE_LIMIT_BURST`, `RATE_LIMIT_REFILL_INTERVAL` — must parse as integers
  greater than zero. Zero, negatives, non-numeric values, and values too large to represent (a
  `RATE_LIMIT_REFILL_INTERVAL` above 9,223,372,036 seconds, or, on a 32-bit platform, a
  `RATE_LIMIT_BURST` above the platform's `int`) fall back to the default with a `WARN`.
- `RATE_LIMIT_REFILL_INTERVAL` is parsed with `strconv.Atoi` and multiplied by `time.Second`.
  Duration strings such as `500ms` or `1s` are **invalid** and fall back to the default.
- `LOG_LEVEL` accepts the names `log/slog` understands, case-insensitively, including offsets such
  as `debug-2`. Anything unrecognized falls back to `info` silently, since the logger does not exist
  yet at the point the level is read. It is read twice with the same result: once when the `server`
  package initializes, so package-level code has a logger at all, and once in `main` to install the
  process-wide default.

### Origin matching

`ALLOWED_ORIGINS` entries and the incoming `Origin` header are both normalized to
`scheme://host` with scheme and host lowercased, then compared exactly.

- The port is part of the host: `http://localhost:8080` does not match `http://localhost:3000`.
- Paths are ignored: `https://example.com/app` is stored as `https://example.com`.
- Entries with no scheme or no host (`example.com`, `localhost:8080`) are rejected when the hub is
  built, with an `ignoring invalid origin in configuration` warning.
- A request with **no** `Origin` header is rejected. Browsers always send one; non-browser clients
  must set it explicitly.
- `*` anywhere in the list allows every origin, including requests whose `Origin` is otherwise
  unparseable. Rejected handshakes return `403 Forbidden`.

## Values that are not configurable

These are compile-time constants. Changing them requires editing the source.

| Value                        | Setting                    | Location                    |
| ---------------------------- | -------------------------- | --------------------------- |
| WebSocket read/write buffers | 1024 bytes each, write buffers pooled across connections | `internal/server/handlers.go` |
| Client send queue depth      | 256 messages               | `internal/server/client.go`   |
| Ping interval                | 54s                        | `internal/server/client.go`   |
| Read deadline                | 60s, reset on each pong     | `internal/server/client.go`   |
| Write deadline               | 10s per write               | `internal/server/client.go`   |
| HTTP read / write timeout    | 15s each                   | `internal/server/service.go` |
| HTTP read header timeout     | 5s                         | `internal/server/service.go` |
| HTTP idle timeout            | 60s                        | `internal/server/service.go` |
| HTTP max header bytes        | 64 KiB                     | `internal/server/service.go` |
| Shutdown budget              | 15s HTTP + 15s hub, 30s cap | `internal/server/service.go` |

## Examples

Local development, allowing a separate front-end dev server:

```bash
export SERVER_PORT=:8080
export ALLOWED_ORIGINS=http://localhost:8080,http://localhost:3000
export MAX_MESSAGE_SIZE=512
export RATE_LIMIT_BURST=5
export RATE_LIMIT_REFILL_INTERVAL=1
export LOG_LEVEL=debug
```

Production behind a reverse proxy on loopback:

```bash
export SERVER_PORT=127.0.0.1:8080
export ALLOWED_ORIGINS=https://chat.example.com
export MAX_MESSAGE_SIZE=1024
export RATE_LIMIT_BURST=10
export RATE_LIMIT_REFILL_INTERVAL=2
export LOG_LEVEL=info
```

## Related

- [API reference](api.md) — what the limits mean on the wire
- [Security hardening](../guides/security-hardening.md) — how to choose values
- [Architecture overview](../architecture/overview.md) — why the limits exist
