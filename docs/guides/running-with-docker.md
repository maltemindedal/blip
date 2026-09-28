# Running with Docker

The repository ships a multi-stage [`Dockerfile`](../../Dockerfile) and a
[`docker-compose.yml`](../../docker-compose.yml) for local use.

## Compose (quickest path)

```bash
docker compose up -d --build
docker compose logs -f blip
```

This builds the image, publishes port 8080, and applies the environment block in
`docker-compose.yml` — the defaults plus `http://localhost:3000` as a second allowed origin. Visit
<http://localhost:8080/test> to confirm.

Stop and remove it:

```bash
docker compose down
```

### Using a .env file instead

`docker-compose.yml` has a commented `env_file` block. Uncomment it and drop the inline
`environment:` list to configure the container from `.env`:

```yaml
    # environment:      # remove or comment out
    env_file:
      - .env
```

```bash
cp .env.example .env
# edit .env
docker compose up -d
```

The Go process never reads `.env` on its own — only Compose (or `docker run --env-file`) does.

## Plain Docker

```bash
docker build -t blip:latest .

docker run -d \
  --name blip \
  -p 8080:8080 \
  -e ALLOWED_ORIGINS="https://chat.example.com" \
  -e MAX_MESSAGE_SIZE=1024 \
  -e RATE_LIMIT_BURST=10 \
  --restart unless-stopped \
  blip:latest
```

`make docker-build` and `make docker-run` wrap the same commands, tagging the image with the
Makefile's `VERSION` variable (`dev` unless you override it).

Keep `SERVER_PORT` at its default inside the container. The image's `HEALTHCHECK` and the Compose
health check both probe `http://localhost:8080/`, so changing the internal port breaks them — remap
on the host side with `-p 9090:8080` instead.

## What the image contains

**Build stage** — `golang:1.26.8-alpine`, compiles with `CGO_ENABLED=0 -trimpath -ldflags="-s -w"`
for a static, stripped binary. `GOOS` and `GOARCH` come from buildx's `TARGETOS`/`TARGETARCH`
arguments, defaulting to `linux/amd64`.

**Runtime stage** — `alpine:3.24` with `ca-certificates`, `tzdata`, and `wget`, running as the
non-root user `blip` (UID 1000). Entry point is `/app/blip`. The health check runs
`wget --spider` against `/` every 30 seconds after a 5-second grace period.

The Compose service additionally runs with a read-only root filesystem, all capabilities dropped, and
`no-new-privileges`. The server writes nothing to disk, so nothing needs a writable mount.

The Go version is pinned in the Dockerfile and must be bumped together with `go.mod`, the README and
the other docs that name it when the toolchain moves. CI reads its version from `go.mod` and needs
no change.

## Behind a reverse proxy

Do not publish 8080 to the internet. Expose it only to the proxy:

```yaml
services:
  blip:
    build: .
    expose:
      - "8080"          # container network only, no host port
    environment:
      - ALLOWED_ORIGINS=https://chat.example.com
    networks:
      - web

networks:
  web:
    external: true
```

Proxy configuration — including the WebSocket upgrade headers and long timeouts that `/ws` needs —
is in [Deploying to production](deploying-to-production.md).

## Operations

```bash
docker compose restart blip
docker inspect --format='{{.State.Health.Status}}' blip-server
docker exec -it blip-server /bin/sh
```

The container writes `log/slog` text records (`key=value`, each with a `level=`) to stderr, which
`LOG_LEVEL` controls; the Docker logging driver captures both streams, so ship them straight to your
log collector.

## Related

- [Configuration reference](../reference/configuration.md) — every environment variable
- [Deploying to production](deploying-to-production.md) — TLS, proxying, systemd
- [Security hardening](security-hardening.md) — what to lock down before exposing the server
