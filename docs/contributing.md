# Contributing

Thanks for wanting to help. This page covers development setup, the checks your change must pass,
and how a pull request gets reviewed.

## Development setup

Prerequisites: Go 1.26.8 or later, Git, and optionally GNU Make.

```bash
git clone https://github.com/maltemindedal/blip.git
cd blip
make install-tools
make help
```

`make install-tools` installs, with `go install`:

| Tool          | Used for                            |
| ------------- | ----------------------------------- |
| golangci-lint | Linting (`make lint`)                |
| govulncheck   | Vulnerability scanning               |
| gosec         | Security static analysis             |
| goimports     | Import grouping                      |
| air           | Rebuild-on-save (`make dev`)         |

The binaries land in `$(go env GOPATH)/bin` — make sure that is on your `PATH`.

## The edit loop

```bash
make dev      # rebuild and restart on every save, via .air.toml
```

Air watches `.go`, `.tpl`, `.tmpl`, and `.html` files, ignores `test/`, `bin/`, and `tmp/`, and
rebuilds to `tmp/main`. Build errors go to `build-errors.log`.

Without air, `make run` rebuilds and runs once.

## Before you push

```bash
make ci-local
```

That is `clean fmt vet lint test-coverage security-scan deps-check build` — a superset of what CI
runs, and slow because `clean` purges the Go build and module caches. The quicker loop:

```bash
make fmt && make vet && make lint && make test
```

Checklist:

- [ ] `make fmt` leaves no changes
- [ ] `make lint` is clean
- [ ] `make test` passes, including `-race`
- [ ] `make security-scan` is clean
- [ ] `go.mod`/`go.sum` are tidy (`go mod tidy` produces no diff — CI enforces this)
- [ ] Documentation updated for any behavior or configuration change
- [ ] New behavior has tests

## Code standards

Follow [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments), and match what is already
in `internal/server`:

- **Formatting** is `gofmt`; imports are grouped stdlib, external, internal.
- **Exported identifiers carry doc comments** starting with the identifier name. Each file in
  `internal/server` also opens with a `// Package server ...` comment describing that file's role —
  keep that pattern when adding a file.
- **Errors are always checked**, and wrapped with context: `fmt.Errorf("shutdown http server: %w", err)`.
  `errcheck` enforces the first half.
- **Functions stay small.** `client.go` and `hub.go` decompose their pumps into one-purpose helpers;
  new code should read the same way.
- **Concurrent state is guarded** — either owned by a single goroutine (as the hub's run loop owns
  the registry) or behind a mutex. Never leave a blocking send without a `case <-shutdown` escape.

Linters enabled in `.golangci.yml`, grouped as the file groups them:

- **Correctness** — `errcheck`, `govet` (with `nilness`, `shadow`, `unusedwrite` and `sortslice` on
  top of its defaults), `staticcheck`, `unused`, `ineffassign`, `bodyclose`, `errorlint`,
  `errname`, `wastedassign`, `nilerr`
- **Security** — `gosec`
- **Style and modernization** — `revive`, `misspell`, `unconvert`, `copyloopvar`, `intrange`,
  `usestdlibvars`, `perfsprint`, `sloglint`, `nolintlint`
- **Tests** — `thelper`, `usetesting`

Formatting is enforced separately by the `formatters` block (`gofmt`, `goimports`).

The repository also carries [`AGENTS.md`](../AGENTS.md), guidance for AI coding assistants: prefer
the minimal change, do not refactor adjacent code, match the surrounding style.

## Tests

New behavior needs tests; bug fixes need a test that fails before the fix. See
[Testing](guides/testing.md) for the layout, the helpers, and the coverage targets.

## Branches and commits

```bash
git checkout -b feature/short-description
```

Prefixes in use: `feature/`, `fix/`, `docs/`, `refactor/`, `test/`, `chore/`.

Commit messages: a short imperative summary on the first line, a blank line, then the why. Reference
issues with `Closes #123`.

## Pull requests

Open the PR against `main`. Describe what changed and why, how you tested it, and link related
issues. CI must be green and a maintainer must approve before merge.

## Continuous integration

`.github/workflows/ci.yml` runs on pushes and pull requests targeting `main` and `develop`:

| Job          | What it does                                                                  |
| ------------ | ----------------------------------------------------------------------------- |
| `test`       | Verifies the module is tidy, builds, runs `go test -race -shuffle=on` with coverage, uploads to Codecov |
| `bench`      | Runs every benchmark once as a compile-and-run smoke test                      |
| `lint`       | golangci-lint v2.13.2 against `.golangci.yml`                                  |
| `vulncheck`  | `govulncheck ./...`                                                            |
| `docker`     | Builds the image and scans it with Trivy, uploading SARIF to the Security tab   |

Every job fails the run on its own, so there is no separate gate job. Runs are grouped per ref with
`cancel-in-progress`, so pushing again supersedes a run still in flight.

The Go version comes from `go.mod` via `go-version-file`, so the workflow never needs its own pin.
`govulncheck` reports vulnerabilities in the Go toolchain itself, so a stdlib advisory fails CI until
the toolchain is raised — and that means `go.mod`, `Dockerfile`, `README.md`, and every page under
`docs/` that names the version, together (`git grep` the old version to find them).

## Reporting issues

**Bugs** — include what you expected, what happened, exact reproduction steps, your OS and
`go version`, the commit you are on, and relevant server log lines.

**Features** — describe the problem before the solution, plus the use case and any alternatives you
considered.

**Security vulnerabilities** — do not open a public issue. See
[Security hardening](guides/security-hardening.md#reporting-a-vulnerability).

## Related

- [Testing](guides/testing.md) — running and writing tests
- [Architecture overview](architecture/overview.md) — how the server is put together
- [Make targets reference](reference/make-targets.md) — every automation entry point
- [Building and releasing](guides/building-and-releasing.md) — cutting a build
