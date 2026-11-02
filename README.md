# bridge

The Firmary **local bridge**: a small program that runs on a person's own computer (or their own always-on server)
and lets Firmary use **their own** Claude or ChatGPT plan. It keeps one outgoing connection to Firmary's relay and,
only when that person asks for something in Firmary, runs their provider's app with their sign-in and streams the
answer back.

People see it as the **Firmary Helper**. That name is not final: it lives in one constant,
[`internal/brand/brand.go`](internal/brand/brand.go), and a test fails if any other Go file spells it out.

Status: **first slice, not released, not signed, not for use yet.**

## Where this comes from

- Design: design studio `5877e3252806` ("local bridge" walkthrough, from chat #1825). The screens this repo serves
  are "Always-on server" (the only terminal people ever see), "Helper on this computer" (restart, what the firm
  sees, permissions) and "On the computer" (installers on macOS, Windows and Ubuntu).
- Plan: `reviews/personal-ai-relay-build-plan-2026-11-01.md` (decisions G1–G11, slices S0–S18). This repo is the
  plan's Helper repo ("H").
- History: the S2 skeleton was built and security-reviewed in a local `firmary-helper` repo
  (`reviews/firmary-helper-s2-security-review-2026-11-01.md`); its commits are kept here as they were.
- Provenance: all the Go code is new, written for this project (first in that local repo, from 2026-11-01). Nothing
  is copied from another project. The only copied files are data: the relay test vectors and fixtures in
  `internal/protocol/testdata` (from Firmary's own `firmary-os`, `packages/validators/ai-relay/helper/fixtures`),
  Anthropic's public release-signing key `internal/driver/claudecode/anthropic-release-key.asc` and the checksums
  in its `pin.go` (from Anthropic's signed release manifest), and the checksums in `internal/driver/codex/pin.go`
  (GitHub's published digests of OpenAI's Codex `rust-v0.166.0` release archives, and of the executables inside
  them). Go dependencies are BSD-licensed (`gorilla/websocket`
  BSD-2-Clause; `ProtonMail/go-crypto`, `cloudflare/circl`, `golang.org/x/crypto`, `golang.org/x/sys`
  BSD-3-Clause), which Apache-2.0 allows.

## What works in this slice

- **Device key.** An Ed25519 key, created on first run in a `0600` file under the data directory. No Firmary secret
  is ever stored on disk.
- **Pairing by code match.** The Helper commits to a random value, the relay sends its own, the Helper reveals its
  value, and both sides derive the same `ABCD-EFGH` code. Firmary shows that code beside the computer that asked;
  the person pairs there only if it matches the Helper's. Nobody types or pastes the code, and no code is ever on a
  command line. It is new on every connection. The Helper prints it only to a terminal, never to a service log,
  then prints the person and firm it paired to.
- **One outgoing connection.** A `wss://` WebSocket to the relay with reconnect and backoff, plus `hello`. Nothing
  listens on the computer and no ports are opened.
- **Its own pinned Claude Code.** The Helper installs its own copy from Anthropic's release bucket and checks it
  against Anthropic's signed manifest *and* a checksum Firmary pins. It runs that copy with the updater off, a
  separate profile, no tools, a cleaned environment, and Firmary's system prompt in place of Claude Code's own. The person's own Claude Code install and login are never
  touched.
- **Its own pinned Codex (driver only).** The Codex driver installs one executable from OpenAI's official GitHub
  release, checked against two checksums Firmary pins (the release archive and the executable in it). Each profile
  has its own `CODEX_HOME` with the update check off. Sign-in is Codex's own ChatGPT sign-in, by browser or by device
  link. A turn is `codex exec --json` with the prompt on stdin, a cleaned environment, Firmary's system prompt in place of
  Codex's built-in instructions, and settings that leave
  nothing able to act on the computer: no shell, MCP, apps, plugins, hooks, skills, memories, web search, project
  instruction files or saved sessions. The one tool Codex always offers, `apply_patch`, runs in a read-only sandbox
  with approvals off, so every write it tries is refused. The Helper doesn't run it yet (see below).
- **Turns.** A turn runs only with a ticket signed by Firmary's pinned key that is for this device and its paired
  user, unexpired, unused and matches the prompt.
- **The terminal screen.** The Helper prints the design's "Always-on server" transcript, changed to the secure
  pairing flow: name, version and computer, `ok  Reached Firmary (outgoing only, nothing to open)`, the code it
  derived with "Check that Firmary shows the same code, then pair there", a "the code changed" note after a
  reconnect, and `ok  Paired to <person> · <firm>`. The code is in [`internal/console`](internal/console); its
  package comment lists where and why it differs from the design, and tests pin the copy.

## What is stubbed or not built yet

| Design piece                                                   | Where it lands                       |
| -------------------------------------------------------------- | ------------------------------------ |
| Firmary's relay, pairing API and ticket signer                 | firmary-os S3 (here: fake relay)     |
| Real relay addresses and ticket keys                           | S3; with no pinned key every turn is refused |
| Codex in the Helper: `helper-codex` adapter, signin/signout over the relay | S9 follow-up (driver is built) |
| Codex limit windows (percent used); `exec --json` reports none | Needs `codex app-server`, legal sign-off |
| Claude Code sign-in from the Helper, test request, usage bars  | S9/S10 and firmary-os S11            |
| Always-on server: one-line install, start with the server, "Always on" | S10                          |
| Silent self-update                                             | S5                                   |
| Signed installers (`.dmg`, `.exe`, `.deb`), menu-bar/tray icon, Helper window | S15                   |
| Health card, Devices list, admin view                          | firmary-os S11 (not in this repo)    |

## What it never logs

Local logs (`logs/helper.log`, 5 x 1 MB, rotating) carry metadata only: timestamps, event type, ticket id,
provider, model, duration, token counts, limit windows, error class and pattern name, versions.

Never logged: prompts, context, outputs, file contents, tokens and credentials, sign-in URLs and codes, the
pairing code, the person's and firm's names, environment variables, raw provider error text, and the stdout or stderr of `claude`.
The logger has no free-text field, and tests enforce this.

## Layout

| Path                         | What                                                         |
| ---------------------------- | ------------------------------------------------------------ |
| `cmd/firmary-helper`         | The Helper                                                   |
| `cmd/fake-relay`             | A local fake relay for development (loopback only)           |
| `internal/brand`             | The product name (one constant)                              |
| `internal/console`           | What the Helper prints to the person, in the design's words  |
| `internal/devicekey`         | Device key                                                   |
| `internal/fsperm`            | Data directory and key ownership checks                      |
| `internal/pairing`           | Pairing record and code-match derivation                     |
| `internal/protocol`          | Relay messages, stream events, prompt hash                   |
| `internal/ticket`            | Turn ticket verification (EdDSA JWT, single use)             |
| `internal/relay`             | Relay client: auth, pairing, hello, reconnect                |
| `internal/driver/claudecode` | Claude Code driver: Ensure, Status, Run, environment scrub   |
| `internal/driver/prompt`     | Firmary's default system prompt for both provider apps (`firmary-system-prompt.md`, placeholder wording) |
| `internal/driver/codex`      | Codex driver: Ensure, Status, SignIn/SignOut, Run, environment scrub |
| `internal/helper`            | Turn policy: ticket and account checks, then the driver      |
| `internal/logx`              | The metadata-only local log                                  |
| `internal/fakerelay`         | Fake relay used by tests and `cmd/fake-relay`                |

Protocol context strings such as `firmary-helper-auth-v1` are part of the wire contract with Firmary's relay
(see `SCHEMA-CHANGES.md`), so they don't follow the product name.

## Develop

Go version: see `go.mod`.

```sh
go test ./...
go run ./cmd/fake-relay            # prints a ws:// URL and a ticket-key file
go run ./cmd/firmary-helper run --env local --relay-url ws://127.0.0.1:PORT/ --ticket-key-file PATH --data-dir ~/.firmary-helper-dev
```

The first `run` downloads the pinned Claude Code (about 290 MB). The real-Claude-Code tests are opt-in: set
`FIRMARY_HELPER_REAL_CLAUDE` to an install directory. The real-Codex tests (about 120 MB) are opt-in the same way
with `FIRMARY_HELPER_REAL_CODEX`.

CI (`.github/workflows/ci.yml`) runs gofmt, vet, the race tests, and builds for macOS, Windows and Linux on
GitHub-hosted runners.

## Licence

Apache-2.0. See `LICENSE` and `NOTICE`.
