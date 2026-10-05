# Blip Documentation

Everything written about Blip, grouped by what you are trying to do. Start with
[Getting started](getting-started.md) if the server has never run on your machine.

## Tutorial — learning by doing

| Document                              | What it covers                                              | For        |
| ------------------------------------- | ----------------------------------------------------------- | ---------- |
| [Getting started](getting-started.md) | Clone, build, run, and chat between two browser tabs in ~10 minutes | Newcomers |

## How-to guides — task-oriented

| Document                                                     | What it covers                                                    | For                 |
| ------------------------------------------------------------ | ----------------------------------------------------------------- | ------------------- |
| [Connecting a client](guides/connecting-a-client.md)         | Working examples in JavaScript, Go, Python, and websocat           | Client developers   |
| [Running with Docker](guides/running-with-docker.md)         | Compose, plain `docker run`, image contents, proxy-facing setup    | Anyone deploying    |
| [Deploying to production](guides/deploying-to-production.md) | Nginx and Caddy, TLS, systemd, firewall, host tuning, scaling limits | Operators         |
| [Security hardening](guides/security-hardening.md)           | Origins, rate limits, size limits, scanning, what the server does *not* protect | Operators |
| [Building and releasing](guides/building-and-releasing.md)   | Local builds, cross-compilation, release flags, checksums          | Maintainers         |
| [Testing](guides/testing.md)                                 | Suite layout, running tests, coverage, helpers, conventions        | Contributors        |

## Reference — lookup

| Document                                            | What it covers                                                       | For          |
| --------------------------------------------------- | -------------------------------------------------------------------- | ------------ |
| [Configuration](reference/configuration.md)         | Every environment variable, default, validation rule, and hard-coded constant | Everyone |
| [API](reference/api.md)                             | All three endpoints, the message protocol, and every failure mode      | Client developers |
| [Make targets](reference/make-targets.md)           | Every target in the Makefile and what it actually runs                 | Contributors |

## Explanation — understanding

| Document                                          | What it covers                                                        | For          |
| ------------------------------------------------- | --------------------------------------------------------------------- | ------------ |
| [Architecture overview](architecture/overview.md) | Components, message flow, lifecycle, concurrency model, design trade-offs | Contributors |

## Contributing

[Contributing](../CONTRIBUTING.md) — development setup, code standards, the pre-push checklist, and
what CI enforces.

## Elsewhere in the repository

- [`.env.example`](../.env.example) — annotated configuration template
- [`AGENTS.md`](../AGENTS.md) — working guidelines for AI coding assistants
- [`test/README.md`](../test/README.md) — pointer to the testing guide
- [`.vscode/README.md`](../.vscode/README.md) — note on VS Code YAML validation warnings
