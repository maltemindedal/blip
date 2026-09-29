# Deploying to Production

Run Blip behind a TLS-terminating reverse proxy, supervised by systemd or Docker.

Docker specifics live in [Running with Docker](running-with-docker.md); this guide covers the proxy,
TLS, process supervision, and host tuning that apply either way.

## Target shape

```
Internet ──TLS──► Nginx or Caddy ──HTTP──► Blip (127.0.0.1:8080)
```

The proxy terminates TLS, and Blip binds to loopback only. Blip itself speaks no TLS and has no
authentication, so it must never be the public listener.

## Checklist

- [ ] Build a stripped binary (`make release`) or image (`docker build`)
- [ ] `SERVER_PORT=127.0.0.1:8080` so the process is unreachable from outside the host
- [ ] `ALLOWED_ORIGINS` set to your real front-end origins — never `*`
- [ ] Reverse proxy configured with WebSocket upgrade headers and long read timeouts
- [ ] TLS certificate installed and auto-renewing
- [ ] Process supervised with restart-on-failure
- [ ] `/test` blocked at the proxy
- [ ] Firewall blocks direct access to 8080
- [ ] Logs shipped somewhere durable

## Configure the server

```bash
SERVER_PORT=127.0.0.1:8080
ALLOWED_ORIGINS=https://chat.example.com,https://www.example.com
MAX_MESSAGE_SIZE=1024
RATE_LIMIT_BURST=10
RATE_LIMIT_REFILL_INTERVAL=1
```

Origins are matched exactly on `scheme://host`, and the port is part of the host — `https://example.com`
does not cover `https://www.example.com`. See the
[configuration reference](../reference/configuration.md#origin-matching).

## Nginx

`/etc/nginx/sites-available/blip`:

```nginx
upstream blip_backend {
    server 127.0.0.1:8080;
}

server {
    listen 443 ssl;
    http2 on;
    server_name chat.example.com;

    ssl_certificate     /etc/letsencrypt/live/chat.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/chat.example.com/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_session_cache   shared:SSL:10m;

    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;
    add_header X-Frame-Options "DENY" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;

    location /ws {
        proxy_pass http://blip_backend;
        proxy_http_version 1.1;

        # Required for the upgrade to succeed
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_set_header Host              $host;
        proxy_set_header Origin            $http_origin;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # WebSockets are long-lived; the default 60s read timeout would cut them
        proxy_read_timeout 86400;
        proxy_send_timeout 86400;
        proxy_buffering    off;
    }

    # Development page — not for production
    location /test { return 404; }

    location / {
        proxy_pass http://blip_backend;
        proxy_set_header Host            $host;
        proxy_set_header X-Real-IP       $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }

    access_log /var/log/nginx/blip-access.log;
    error_log  /var/log/nginx/blip-error.log;
}

server {
    listen 80;
    server_name chat.example.com;
    return 301 https://$server_name$request_uri;
}
```

```bash
sudo ln -s /etc/nginx/sites-available/blip /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

The `Origin` header must survive the hop — Blip rejects handshakes without it. Nginx forwards it
by default; the explicit `proxy_set_header Origin $http_origin` guards against a global config that
strips it.

## Caddy

Caddy obtains and renews certificates automatically and needs no special WebSocket configuration.

```caddy
chat.example.com {
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains"
        X-Frame-Options "DENY"
        X-Content-Type-Options "nosniff"
        Referrer-Policy "strict-origin-when-cross-origin"
    }

    respond /test 404

    reverse_proxy localhost:8080

    log {
        output file /var/log/caddy/blip.log
        format json
    }
}
```

## TLS with Let's Encrypt

For Nginx:

```bash
sudo apt install certbot python3-certbot-nginx
sudo certbot --nginx -d chat.example.com
sudo certbot renew --dry-run
```

Certbot installs its own renewal timer. Caddy handles renewal internally — nothing to configure.

Once TLS is live, clients dial `wss://chat.example.com/ws`. Browsers refuse plain `ws://` from an
HTTPS page.

## systemd

`/etc/systemd/system/blip.service`:

```ini
[Unit]
Description=Blip WebSocket Server
Documentation=https://github.com/maltemindedal/blip
After=network.target

[Service]
Type=simple
User=blip
Group=blip
WorkingDirectory=/opt/blip
ExecStart=/opt/blip/bin/blip
Restart=always
RestartSec=10

Environment=SERVER_PORT=127.0.0.1:8080
Environment=ALLOWED_ORIGINS=https://chat.example.com
# or: EnvironmentFile=/etc/blip/blip.env

# Shutdown drains HTTP then the hub, up to 30s total
TimeoutStopSec=45

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/blip
LimitNOFILE=65536

StandardOutput=journal
StandardError=journal
SyslogIdentifier=blip

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd -r -s /bin/false blip
sudo mkdir -p /opt/blip/bin
sudo cp bin/linux/blip-amd64 /opt/blip/bin/blip
sudo chown -R blip:blip /opt/blip

sudo systemctl enable --now blip
sudo systemctl status blip
sudo journalctl -u blip -f
```

systemd sends `SIGTERM` on stop, which triggers the graceful shutdown path: stop accepting
connections, close the HTTP server (15s budget), then close every WebSocket connection (15s budget),
capped at 30s overall. `TimeoutStopSec` must exceed that, or systemd will `SIGKILL` mid-drain.

## Firewall

```bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw deny 8080/tcp
sudo ufw enable
```

Binding to `127.0.0.1:8080` already prevents external access; the deny rule is defense in depth.

## Health checks and monitoring

`GET /` returns `200` with `Blip server is running!` and is what the Docker and Compose health
checks poll:

```bash
curl -fsS https://chat.example.com/ || echo "unhealthy"
```

It confirms only that the HTTP listener answers — it reports nothing about hub state or connection
count. The server exposes **no metrics endpoint**, so operational visibility comes from the logs. They are
`log/slog` key-value records written to **stderr**, at the level set by `LOG_LEVEL` (default `info`):

```
time=2026-07-23T15:51:57.081+02:00 level=INFO msg="client registered" addr=[::1]:65503 total_clients=1
```

Messages to alert on:

| `msg`                                            | Level | Meaning                                      |
| ------------------------------------------------ | ----- | -------------------------------------------- |
| `blocked WebSocket connection from disallowed origin` | WARN | Misconfigured `ALLOWED_ORIGINS` or an attack |
| `rate limit exceeded; discarding message`        | WARN  | A client is flooding (once per episode)       |
| `dropping client with a full send buffer`        | WARN  | A slow consumer was dropped                   |
| `hub shutdown timed out...`                      | ERROR | Shutdown did not drain within its budget      |

`rate limit exceeded; discarding message` is logged when a client's first message is discarded after
one was last allowed, so it marks the start of throttling, not each dropped message: a client that
keeps flooding produces at most one line per message the limiter lets through, not one per frame.
Do not count these lines as a measure of dropped messages.

`client registered` and `client unregistered` carry `total_clients`, which is the closest thing to a
connection-count gauge. Do not run production at `LOG_LEVEL=debug`: it logs every inbound message and
every broadcast, which is both a throughput cost and a privacy exposure, since message bodies end up
in the log.

## Scaling

Each connection costs two goroutines (a read pump and a write pump) plus a 256-message send buffer.
Vertical scaling is the simple path — raise `LimitNOFILE` and the kernel limits below.

```
# /etc/security/limits.conf
blip soft nofile 65536
blip hard nofile 65536
```

```
# /etc/sysctl.conf
net.core.somaxconn = 4096
net.ipv4.tcp_max_syn_backlog = 4096
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
```

Apply with `sudo sysctl -p`.

**Horizontal scaling does not work as-is.** The hub is in-process, so a client on instance A never
receives messages from a client on instance B. Running several instances behind a load balancer
partitions the chat room. Multi-instance operation would require a shared bus (Redis pub/sub, NATS)
that the codebase does not have.

## Rollback

The deployable artifact is a single static binary with no migrations or persistent state, so
rollback is: stop the service, swap the binary, start it. Keep the previous binary next to the
current one, and version-control the environment file.

## Related

- [Running with Docker](running-with-docker.md) — container deployment
- [Security hardening](security-hardening.md) — origin, rate limit, and TLS rationale
- [Configuration reference](../reference/configuration.md) — every environment variable
- [Building and releasing](building-and-releasing.md) — producing the binary
