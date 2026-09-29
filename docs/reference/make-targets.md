# Make Targets Reference

Every target defined in the [`Makefile`](../../Makefile). Run `make help` for the same list generated
from the file itself.

The Makefile switches `SHELL` to `pwsh.exe` on Windows, so the build, clean, and cross-compile
targets work identically on Windows, macOS, and Linux. GNU Make is required on Windows
(`choco install make`).

## Build

| Target                | What it does                                                             |
| --------------------- | ------------------------------------------------------------------------ |
| `build`               | `fmt` + `vet`, then build to `bin/blip` (`bin/blip.exe` on Windows)   |
| `build-raw`           | Same build without the `fmt`/`vet` prerequisites                         |
| `build-current`       | Build for the host platform with `CGO_ENABLED=0`                         |
| `build-linux`         | Cross-compile → `bin/linux/blip-amd64`                                  |
| `build-linux-arm64`   | Cross-compile → `bin/linux/blip-arm64`                                  |
| `build-darwin`        | Cross-compile → `bin/MacOS/blip-amd64`                                  |
| `build-darwin-arm64`  | Cross-compile → `bin/MacOS/blip-arm64`                                  |
| `build-windows`       | Cross-compile → `bin/windows/blip-amd64.exe`                            |
| `build-windows-arm64` | Cross-compile → `bin/windows/blip-arm64.exe`                            |
| `build-all`           | All six cross-compile targets                                            |
| `release`             | `clean fmt vet lint test`, then trimmed, stripped static builds plus SHA256 checksums |
| `list-platforms`      | `go tool dist list`                                                       |
| `clean`               | Delete `bin/` and run `go clean -cache -testcache -modcache`              |

All builds stamp `main.Version`, `main.Commit`, and `main.BuildTime` via `-ldflags`. The `main`
package declares all three (`cmd/server/main.go`) and logs them on startup, so the stamped values
show up in the first log line. `release` additionally passes `-s -w` to strip the symbol table and
DWARF data.

> **Note:** `clean` purges the module cache for every Go project on the machine, not just this one.
> The next build re-downloads dependencies.

## Run

| Target       | What it does                                             |
| ------------ | -------------------------------------------------------- |
| `run`        | `build`, then run the binary in the foreground            |
| `dev`        | Run under [air](https://github.com/air-verse/air) with rebuild-on-save (config: `.air.toml`) |
| `docker-build` | `docker build -t blip:$(VERSION)` and tag `:latest`   |
| `docker-run` | `docker-build`, then run with `-p 8080:8080`              |

## Test

| Target                       | What it does                                                     |
| ---------------------------- | ---------------------------------------------------------------- |
| `test`                       | `go test -v -race ./...`                                          |
| `test-unit`                  | `./test/unit/...` only                                            |
| `test-integration`           | `./test/integration/...` only                                     |
| `test-coverage`              | All tests, including internal ones → `coverage.out` + `coverage.html` + a per-function summary |
| `test-coverage-unit`         | Same, unit tests only → `unit-coverage.*`                         |
| `test-coverage-integration`  | Same, integration tests only → `integration-coverage.*`           |
| `race`                       | `go test -race ./...` without `-v`                                |
| `bench`                      | `go test -run '^$' -bench=. -benchmem ./...` — hot-path benchmarks in `internal/server` |

## Quality and dependencies

| Target          | What it does                                                    |
| --------------- | --------------------------------------------------------------- |
| `fmt`           | `go fmt ./...`                                                   |
| `vet`           | `go vet ./...`                                                   |
| `lint`          | `golangci-lint run --config .golangci.yml`                       |
| `lint-fix`      | Same with `--fix`                                                |
| `security-scan` | `govulncheck ./...` then `gosec ./...`                           |
| `deps-check`    | `go list -u -m all` then `govulncheck ./...`                     |
| `deps-update`   | `go get -u ./...` and `go mod tidy`                              |
| `license-check` | `go-licenses report` → `licenses.txt`                            |
| `deps-graph`    | `go mod graph` piped to Graphviz `dot` → `deps-graph.png`         |
| `all`           | `clean fmt vet lint test build`                                  |
| `ci-local`      | `clean fmt vet lint test-coverage security-scan deps-check build` |

`install-tools` installs golangci-lint, govulncheck, gosec, goimports, and air with `go install`,
each at a pinned version (golangci-lint and govulncheck at the versions CI uses).

`docs` starts a local `godoc` server on <http://localhost:6060>.

> **Note:** `license-check` renders with a `licenses.tpl` template that is not in the repository, and
> `deps-graph` needs Graphviz on `PATH`. Both are best-effort helpers.

## Related

- [Building and releasing](../guides/building-and-releasing.md) — when to use which target
- [Testing](../guides/testing.md) — how the suite is organized
- [Contributing](../contributing.md) — the checks CI runs
