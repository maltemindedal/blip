# Security Hardening

What Blip protects against, how to configure those protections, and what it deliberately does not
do. Exact defaults and parsing rules are in the
[configuration reference](../reference/configuration.md).

## What the server does not provide

Know these before exposing the server:

- **No authentication or authorization.** Any client from an allowed origin can connect and read
  every message. Put authentication in front of it — a proxy that validates a session cookie or
  token before proxying `/ws`.
- **No TLS.** The server speaks plain HTTP. TLS termination belongs to a reverse proxy. See
  [Deploying to production](deploying-to-production.md).
- **No per-IP connection limit.** Rate limiting is per connection, so one client can open many
  connections and multiply its budget. Cap concurrent connections per IP at the proxy.
- **No content filtering.** Message bodies are relayed as-is. The server re-encodes them with
  `encoding/json`, so HTML metacharacters arrive escaped as `<`/`>`/`&`, but that is
  a JSON transport detail, not sanitization: a rendering client must still escape on output. The
  built-in `/test` page uses `textContent` rather than `innerHTML` for exactly this reason.
- **No audit trail.** Logs are structured key-value records, but carry no request IDs or user
  identity — only the remote address.

## Origin validation

Every WebSocket handshake must carry an `Origin` header that matches `ALLOWED_ORIGINS`; failures get
`403 Forbidden` and a log line. This is what stops Cross-Site WebSocket Hijacking (CSWSH): without
it, any page on the internet could open a socket to your server from a visitor's browser, because
the same-origin policy does not apply to WebSockets.

Configure it explicitly in production:

```bash
ALLOWED_ORIGINS=https://chat.example.com,https://www.example.com
```

Rules that catch people out:

- Matching is exact on `scheme://host`, and the port is part of the host. `http://localhost:8080`
  does not match `http://127.0.0.1:8080`.
- Subdomains are not implied. List every origin your front end is served from.
- Entries with no scheme (`example.com`) are dropped at startup with a log line and silently do
  nothing.
- `*` accepts every origin, including `Origin` headers that are not valid URLs (a sandboxed iframe
  or a `file://` page sends the literal `null`). It does **not** accept a request with no `Origin`
  header at all — that is still rejected. Use it only for local experiments.

Verify a deployment:

```bash
# expected: connects
websocat -H='Origin: https://chat.example.com' wss://chat.example.com/ws

# expected: 403
websocat -H='Origin: https://evil.example' wss://chat.example.com/ws
```

Origin validation defends against browsers, not against direct clients — `curl` or a script can send
any `Origin` it likes. It is not authentication.

## Rate limiting

Each connection gets a token bucket holding `RATE_LIMIT_BURST` tokens that refills over
`RATE_LIMIT_REFILL_INTERVAL`. One message costs one token.

**Over-limit messages are discarded. The connection stays open and the client is never told.** Plan
client-side throttling accordingly; there is no error frame and no close code to react to.

```bash
RATE_LIMIT_BURST=10
RATE_LIMIT_REFILL_INTERVAL=1     # 10 msg/s sustained, 10-message burst
```

The interval is whole seconds only, so sub-second refills are not expressible. To allow 20 msg/s,
raise the burst rather than shortening the interval.

Suggested starting points:

| Workload                         | Burst | Interval |
| -------------------------------- | ----- | -------- |
| Human chat                       | 5     | 1s       |
| Chat with typing indicators      | 10–20 | 1s       |
| Machine-generated telemetry      | 50+   | 1s       |

Because the limit is per connection rather than per IP, it protects the server from an accidental
client loop, not from a determined attacker. Pair it with a connection limit at the proxy.

## Message size limits

`MAX_MESSAGE_SIZE` (512 bytes by default) is applied as the WebSocket read limit. Unlike rate
limiting, exceeding it **closes the connection** — the read pump exits with `ErrReadLimit` and the
client is unregistered.

JSON framing counts toward the limit: `{"content":""}` costs 14 bytes, leaving roughly 498 bytes of
text at the default. Raise it when clients send longer messages, and remember that each connection
can also queue 256 outbound messages, so `MAX_MESSAGE_SIZE × 256 × connections` bounds worst-case
buffer memory.

## Automated scanning

`make security-scan` runs both scanners locally:

```bash
govulncheck ./...    # known vulnerabilities in the stdlib and dependencies
gosec ./...          # static analysis for insecure patterns
```

CI runs `govulncheck ./...` on every push and pull request to `main` and `develop`, `gosec` as part
of golangci-lint, and Trivy against the built image, uploading its findings to the repository's
Security tab. Any of these failing fails the run. `govulncheck` reports vulnerabilities in the Go
toolchain itself, so a stdlib advisory fails CI until the Go version is bumped in `go.mod` — which
CI reads directly — plus the `Dockerfile`, the README, and the docs that name the version.

## Production checklist

- [ ] TLS terminated at the proxy; clients dial `wss://`
- [ ] `ALLOWED_ORIGINS` lists real origins, no `*`
- [ ] `SERVER_PORT` bound to loopback, direct access firewalled
- [ ] `/test` blocked at the proxy
- [ ] Authentication enforced in front of `/ws`, if the data warrants it
- [ ] Rate limit and message size sized for the workload
- [ ] Per-IP connection cap at the proxy
- [ ] `govulncheck` green; Go toolchain current
- [ ] Logs shipped and alerting on blocked-origin and rate-limit lines

## Reporting a vulnerability

Do not open a public issue for a security problem. Use a
[private security advisory](https://github.com/maltemindedal/blip/security/advisories/new) on the
repository and include a description, reproduction steps, and impact.

The full policy — supported versions, what to expect, and what is in and out of scope — is in
[`SECURITY.md`](../../SECURITY.md).

## Related

- [Configuration reference](../reference/configuration.md) — defaults and parsing rules
- [Deploying to production](deploying-to-production.md) — TLS, proxy, firewall
- [Architecture overview](../architecture/overview.md) — where each control sits in the request path
