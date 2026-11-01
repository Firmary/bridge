# Firmary Helper

A small open-source program that keeps one outgoing connection from your computer (or your own server) to the
Firmary relay, and runs Anthropic's Claude Code on **your own plan** when **you** ask for something in Firmary.

Status: **early skeleton (slice S2)**. Not released, not signed, not for use yet.

## What it does

- Creates an Ed25519 **device key** on first run (a `0600` file under the Helper's data directory). No Firmary
  secret is ever stored on disk.
- **Pairs** with your Firmary account by **code match**: the Helper commits to a random nonce, the relay sends its
  own, the Helper reveals its nonce, and both sides derive the code from the public key and the two nonces. You
  confirm the same code in Firmary, and the Helper then prints which firm it paired to.
- Keeps one **outbound WebSocket** (`wss://` only) to the relay, reconnecting with backoff, and announces itself
  with `hello`.
- Installs its **own pinned copy of Claude Code** from Anthropic's release bucket, verified against Anthropic's
  GPG-signed release manifest *and* the checksum Firmary pins, and runs it with Anthropic's updater off, its own
  profile directory, **no tools**, and a **scrubbed environment** so it can only use the plan you signed in with.
- Runs a turn only for a ticket signed by Firmary's pinned key, for this device and its paired user, unexpired,
  unused, and matching the prompt hash.

Your own Claude Code install and login are never touched.

## What it never logs

Local logs (`logs/helper.log`, 5 x 1 MB, rotating) carry metadata only: timestamps, event type, ticket id,
provider, model, duration, token counts, limit windows, error class and pattern name, versions.

Never logged: prompts, context, outputs, file contents, tokens and credentials, sign-in URLs and codes, the
pairing code, environment variables, raw provider error text, and the stdout or stderr of `claude`. The logger has
no free-text field, and tests enforce this.

## Layout

| Path                        | What                                                         |
| --------------------------- | ------------------------------------------------------------ |
| `cmd/firmary-helper`        | The Helper                                                   |
| `cmd/fake-relay`            | A local fake relay for development (loopback only)           |
| `internal/devicekey`        | Device key                                                   |
| `internal/pairing`          | Pairing record and code-match derivation                     |
| `internal/protocol`         | Relay messages, stream events, prompt hash                   |
| `internal/ticket`           | Turn ticket verification (EdDSA JWT, single use)             |
| `internal/relay`            | Relay client: auth, pairing, hello, reconnect                |
| `internal/driver/claudecode`| Claude Code driver: Ensure, Status, Run, environment scrub   |
| `internal/helper`           | Turn policy: ticket and account checks, then the driver      |
| `internal/logx`             | The metadata-only local log                                  |
| `internal/fakerelay`        | Fake relay used by tests and `cmd/fake-relay`                |

## Develop

```sh
go test ./...
go run ./cmd/fake-relay            # prints a ws:// URL and a ticket-key file
go run ./cmd/firmary-helper run --env local --relay-url ws://127.0.0.1:PORT/ --ticket-key-file PATH --data-dir /tmp/fh
```

## Licence

Apache-2.0. See `LICENSE` and `NOTICE`.
