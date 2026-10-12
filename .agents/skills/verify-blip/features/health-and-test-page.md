# Health and test page

`GET /` is a plain-text health response. `GET /test` serves the browser page for trying the relay by hand.

## Sub-features

- `health` returns 200 for `GET /`.
- `test-page` returns 200 `text/html` for `GET /test`.

## How to get to it (user POV)

- Open `http://localhost:8080/` or `/test` in a browser.

## Driving it with verify.sh

Preconditions:

- `verify.sh launch` printed `PASS server ready`.

- **Check.** Run `verify.sh health`. Both lines PASS. The bodies are in `health.body` and `testpage.html`.

## Gotchas

- Launch uses `/` as its readiness probe, so a broken health handler shows up as a launch failure.
