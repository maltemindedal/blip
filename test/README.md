# Tests

- `integration/` — a real service over real HTTP/WebSocket sockets
- `testhelpers/` — shared setup and assertion helpers for the integration suite

The unit tests are package-internal, next to the code they cover: `internal/server/*_internal_test.go`.

```bash
make test                # everything, with -race
make test-unit           # unit only (./internal/...)
make test-integration    # integration only
make test-coverage       # coverage.out + coverage.html
```

Suite layout, helper reference, coverage numbers, and conventions for writing new tests:
[docs/guides/testing.md](../docs/guides/testing.md).
