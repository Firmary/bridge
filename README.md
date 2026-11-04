# bridge

The Firmary **local bridge**: a small program that runs on a person's own computer (or their own always-on server)
and lets Firmary use **their own** Claude or ChatGPT plan. It keeps one outgoing connection to Firmary's relay and,
only when that person asks for something in Firmary, runs their provider's app with their sign-in and streams the
answer back.

People see it as **Firmary Link**. The name lives in [`internal/brand/brand.go`](internal/brand/brand.go), and a
test fails if any other Go file spells it out. It was the "Firmary Helper" in the first beta; a computer paired with
that beta moves its data to the new name on first run and stays paired.

## Status

**Beta, unsigned.** Version 0.2.0 is the first Firmary Link release, published as the latest GitHub release. It is
not signed by Apple or Microsoft yet: macOS Gatekeeper and Windows SmartScreen warn the first time it opens. A signed
build is coming. Installed copies update themselves from the release's signed update manifest.

## Download

From the [latest release](https://github.com/Firmary/bridge/releases/latest). The names don't change between
releases, so `https://github.com/Firmary/bridge/releases/latest/download/<file>` always gives the newest:

| File                                                                   | For                                         |
| ---------------------------------------------------------------------- | ------------------------------------------- |
| `Firmary-Link.dmg`                                                     | Mac (Apple silicon and Intel), macOS 13+    |
| `Firmary-Link-Setup.exe`                                               | Windows 10 and 11 (x64 and Arm), per user   |
| `firmary-link_<version>_amd64.deb`, `firmary-link_<version>_arm64.deb` | Linux desktop (Debian, Ubuntu)              |
| `install.sh`, `firmary-link-linux-x64.tar.gz`, `-linux-arm64.tar.gz`   | Linux server or VPS                         |
| `manifest.json`, `manifest.json.sig`, `checksums.txt`                  | The signed update manifest and the SHA-256s |

- **Mac**: open the dmg, drag **Firmary Link** to **Applications** and open it. The first time macOS blocks it: open
  **System Settings › Privacy & Security** and click **Open Anyway**.
- **Windows**: run `Firmary-Link-Setup.exe`; at "Windows protected your PC" click **More info › Run anyway**.
- **Linux server or VPS** (no browser needed on the server):

  ```sh
  curl -fsSL https://github.com/Firmary/bridge/releases/latest/download/install.sh | sh
  ```

  It installs `firmary-link` for the current user (no sudo) after checking it against `checksums.txt`, pairs it
  with Firmary, makes it start with the server (`firmary-link service install`) and offers the Claude or ChatGPT
  sign-in, with a link that works on any browser, even a phone. Later: `firmary-link status`,
  `firmary-link sign-in claude|chatgpt`, `firmary-link service status`.

A `vX.Y.Z` tag (plain semver, 0.2.0 or later) builds and publishes the release (`.github/workflows/release.yml`,
GitHub-hosted runners only); a weekly run re-signs the latest manifest with a fresh expiry.

## Where this comes from

- Design: design studio `5877e3252806` ("local bridge" walkthrough, from chat #1825). The screens this repo serves
  are "Always-on server" (the only terminal people ever see), "Firmary Link on this computer" (restart, what the
  firm sees, permissions; the design still calls it "Helper", the beta's old name) and "On the computer"
  (installers on macOS, Windows and Ubuntu).
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

## What works

- **Device key.** An Ed25519 key, created on first run in a `0600` file under the data directory. No Firmary secret
  is ever stored on disk.
- **Pairing by code match.** The Link commits to a random value, the relay sends its own, the Link reveals its
  value, and both sides derive the same `ABCD-EFGH` code. Firmary shows that code beside the computer that asked;
  the person pairs there only if it matches the Link's. Nobody types or pastes the code, and no code is ever on a
  command line. It is new on every connection. It is shown only in a terminal or the app's menu (with a native
  notification), never in a service log.
- **Accept on the computer.** Confirming the code in Firmary doesn't pair by itself: the relay then offers the
  binding, the Link names the person, their email and the firm, and pairs only when the person says yes (typing
  `yes` in a terminal, or the app's menu or dialog). Anything else sends `pairing_decline` and the Link starts over
  with a new key. A service with no terminal neither accepts nor declines: run `firmary-link pair` in a terminal.
- **One outgoing connection.** A `wss://` WebSocket to the relay with reconnect and backoff, plus `hello`. Nothing
  listens on the computer and no ports are opened, the desktop app included.
- **The desktop app.** With a display (macOS, Windows, a Linux desktop with a tray), `firmary-link` with no command
  is a menu-bar or tray app with no terminal: the pairing code and status (Not paired, Waiting for code, Paired to
  the firm, Connected, Offline), Sign in to Claude, Sign in to ChatGPT, Open at login, Check for updates, Quit.
  Without a display, or with `--headless`, it is the terminal Link.
- **Its own pinned Claude Code.** The Link installs its own copy from Anthropic's release bucket and checks it
  against Anthropic's signed manifest *and* a checksum Firmary pins. It runs that copy with the updater off, a
  separate profile, no tools, a cleaned environment, and Firmary's system prompt in place of Claude Code's own. The
  person's own Claude Code install and login are never touched. Sign-in is `claude auth login --claudeai` in the
  Link's own profile: the browser on a desktop, or a link for any browser plus the code the page shows pasted back.
- **Its own pinned Codex.** One executable from OpenAI's official GitHub release, checked against two checksums
  Firmary pins (the release archive and the executable in it), in the Link's own `CODEX_HOME` with the update check
  off. Sign-in is Codex's own ChatGPT sign-in, by browser or by device code. A turn is `codex exec --json` with the
  prompt on stdin, a cleaned environment, Firmary's system prompt in place of Codex's built-in instructions, and
  settings that leave nothing able to act on the computer: no shell, MCP, apps, plugins, hooks, skills, memories, web
  search, project instruction files or saved sessions. The one tool Codex always offers, `apply_patch`, runs in a
  read-only sandbox with approvals off, so every write it tries is refused.
- **Turns.** A turn runs only with a ticket signed by Firmary's pinned key that is for this device and its paired
  user, unexpired, unused and matches the prompt. The ticket's adapter picks the app: `helper-claude-code` or
  `helper-codex`.
- **`hello`.** Reports both provider apps: `signedIn`, `accountType` (Claude: the Claude plan; Codex: the ChatGPT
  plan type), `orgId` (the Claude organisation or the ChatGPT workspace) and `pinnedVersion`, and is sent again
  after a sign-in or sign-out.
- **Silent self-update.** At start and every few hours, between turns only, the Link reads the signed
  `manifest.json` (firmary-os `UpdateManifest`, detached Ed25519 signature, checked against two pinned update keys,
  current and next). A tampered, foreign-key, replayed or expired manifest is refused. A newer Link in this device's
  rollout downloads, is checked against its SHA-256, passes a self-test and is switched in; if it keeps failing to
  start the Link goes back to the previous version. New provider pins move the pinned Claude Code and Codex the same
  way. Nobody sees anything unless something fails.
- **Always on.** `firmary-link service install|uninstall|status`: a systemd unit (user-level when not root, with
  restart and `MemoryMax`), a launchd agent on macOS, a logon task on Windows. `firmary-link open-at-login on|off`
  for the desktop app.
- **The terminal screen.** The design's "Always-on server" transcript, changed to the secure pairing flow. The copy
  is in [`internal/console`](internal/console); its package comment lists where and why it differs from the design,
  and tests pin it.

## What is not built yet

| Piece                                                              | Where it lands                       |
| ------------------------------------------------------------------ | ------------------------------------ |
| Signed builds: Apple Developer ID and notarization, Authenticode   | S15, once the certificates exist     |
| Firmary-hosted update manifest (today: this repo's release assets) | firmary-os                           |
| Codex limit windows (percent used); `exec --json` reports none     | Needs `codex app-server`, legal sign-off |
| Test request and usage bars                                        | firmary-os S11                       |
| Health card, Devices list, admin view                              | firmary-os S11 (not in this repo)    |
| Dev ticket keys (prod's current and next are pinned)               | With no pinned key every dev turn is refused |

## What it never logs

Local logs (`logs/link.log`, 5 x 1 MB, rotating) carry metadata only: timestamps, event type, ticket id,
provider, model, duration, token counts, limit windows, error class and pattern name, versions.

Never logged: prompts, context, outputs, file contents, tokens and credentials, sign-in URLs and codes, the
pairing code, the person's and firm's names, environment variables, raw provider error text, and the stdout or stderr of `claude`.
The logger has no free-text field, and tests enforce this.

## Layout

| Path                         | What                                                         |
| ---------------------------- | ------------------------------------------------------------ |
| `cmd/firmary-link`           | The Link: desktop app, terminal and commands                 |
| `cmd/link-release`           | Release tool: update keys, writes and signs the manifest     |
| `cmd/fake-relay`             | A local fake relay for development (loopback only)           |
| `internal/brand`             | The product name (one constant)                              |
| `internal/console`           | What the Link prints to the person, in the design's words    |
| `internal/agent`             | The running Link: relay, turns, updates, state for the app   |
| `internal/tray`              | The menu-bar and tray app                                    |
| `internal/notify`            | Native notifications and the pairing question                |
| `internal/service`           | systemd, launchd, logon task; open at login                  |
| `internal/update`            | Signed manifest, rollout, download, switch and roll back     |
| `internal/datadir`           | Data directory, and the move from the Helper beta's          |
| `internal/devicekey`         | Device key                                                   |
| `internal/fsperm`            | Data directory and key ownership checks                      |
| `internal/pairing`           | Pairing record and code-match derivation                     |
| `internal/protocol`          | Relay messages, stream events, prompt and profile hashes     |
| `internal/ticket`            | Turn ticket verification (EdDSA JWT, single use)             |
| `internal/relay`             | Relay client: auth, pairing, hello, reconnect                |
| `internal/driver/claudecode` | Claude Code driver: Ensure, Status, Run, environment scrub   |
| `internal/driver/prompt`     | Firmary's fallback system prompt for both provider apps (`firmary-system-prompt.md`), used when a turn's launch profile has none |
| `internal/launch`            | Launch profile parse and hard ceiling: the named settings Firmary may set per turn, and the `hello` capabilities |
| `internal/driver/codex`      | Codex driver: Ensure, Status, SignIn/SignOut, Run, environment scrub |
| `internal/helper`            | Turn policy: ticket and account checks, then the driver (package name kept) |
| `internal/logx`              | The metadata-only local log                                  |
| `internal/fakerelay`         | Fake relay used by tests and `cmd/fake-relay`                |

Protocol context strings such as `firmary-helper-auth-v1` are part of the wire contract with Firmary's relay
(see `SCHEMA-CHANGES.md`), so they don't follow the product name.

## Develop

Go version: see `go.mod`.

```sh
go test ./...
go run ./cmd/fake-relay            # prints a ws:// URL and a ticket-key file
go run ./cmd/firmary-link run --env local --relay-url ws://127.0.0.1:PORT/ --ticket-key-file PATH --data-dir ~/.firmary-link-dev
```

The first `run` downloads the pinned Claude Code (about 290 MB). The real-Claude-Code tests are opt-in: set
`FIRMARY_HELPER_REAL_CLAUDE` to an install directory. The real-Codex tests (about 120 MB) are opt-in the same way
with `FIRMARY_HELPER_REAL_CODEX`.

CI (`.github/workflows/ci.yml`) runs gofmt, vet, the race tests, and builds for macOS (with cgo, on a hosted
Mac), Windows and Linux on GitHub-hosted runners.

## Licence

Apache-2.0. See `LICENSE` and `NOTICE`.
