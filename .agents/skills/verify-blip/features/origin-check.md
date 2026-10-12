# Origin check

The handshake is refused with 403 unless `Origin` is on `ALLOWED_ORIGINS`. A missing Origin is refused even with `*`.

## Sub-features

- `origin-missing` returns 403 for a handshake with no Origin.
- `origin-allowed` lets an allowed Origin connect.

## How to get to it (user POV)

- Connect to `/ws` from a browser page on another origin, or from a client that sets no Origin.

## Driving it with verify.sh

Preconditions:

- `verify.sh launch` printed `PASS server ready`.

- **Check.** Run `verify.sh relay`. The output shows `PASS handshake without Origin is refused with 403` and `PASS two clients connect with the allowed Origin`.

## Gotchas

- A disallowed but present Origin is not driven yet. Add a `-origin http://evil.example` run to `wsclient` when touching `origin.go`.
