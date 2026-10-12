# Message relay

A message sent to `/ws` reaches every other connected client, normalized to exactly `{"content":...}`. The sender does not get its own message back.

## Sub-features

- `relay-fanout` delivers the message to the other client.
- `relay-normalize` drops unknown fields and escapes `<`, `>` and `&` as `encoding/json` does.
- `relay-no-echo` means the sender receives nothing.

## How to get to it (user POV)

- Open `/test` in two tabs, or connect two WebSocket clients to `/ws` with an allowed `Origin`.

## Driving it with verify.sh

Preconditions:

- `verify.sh launch` printed `PASS server ready`.

- **Relay.** Run `verify.sh relay`. The output shows `PASS receiver gets the normalized, escaped message` and `PASS sender does not receive its own message`.
- **Proof.** `relay.stdout` and the `client registered` lines in `server.log`.

## Gotchas

- Queued messages are packed into one frame separated by `\n`. The client sends one message, so it reads one frame.
- `RATE_LIMIT_BURST` defaults to 5. Sending more in a burst drops messages silently.
