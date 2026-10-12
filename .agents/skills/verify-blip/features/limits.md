# Rate and size limits

Frames over `MAX_MESSAGE_SIZE` close the connection. Messages beyond `RATE_LIMIT_BURST` per `RATE_LIMIT_REFILL_INTERVAL` are dropped silently.

## Sub-features

- `limit-size` closes the connection for an oversized frame. Not automated.
- `limit-rate` drops the 6th message in a burst. Not automated.

## How to get to it (user POV)

- Send a frame over 512 bytes, or more than 5 messages within a second.

## Driving it with verify.sh

Preconditions:

- `verify.sh launch` printed `PASS server ready`.

- **Not automated yet.** There is no step for this. Use `wsclient -payload '<600-byte JSON>'` by hand and expect the receiver to get nothing and the sender's connection to close. Report this feature as unverified until a step exists.

## Gotchas

- `test/integration` covers both limits. This page is about the user-path proof.
