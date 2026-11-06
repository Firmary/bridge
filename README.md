# bridge

The Firmary **local bridge**: a small program that runs on a person's own computer (or their own always-on server)
and lets Firmary use **their own** Claude or ChatGPT plan. It keeps one outgoing connection to Firmary's relay and,
only when that person asks for something in Firmary, runs their provider's app with their sign-in and streams the
answer back.

People see it as **Firmary Bridge**. The name lives in [`internal/brand/brand.go`](internal/brand/brand.go), and a
test fails if any other Go file spells it out. It was the "Firmary Helper" in the first beta; a computer paired with
that beta moves its data to the new name on first run and stays paired. From 0.2.0 to 0.3.1 it was **Firmary Link**
(the code still calls it "the Link"). That rename changed only what people see: the command (`firmary-link`), the
macOS bundle id (`app.firmary.link`), the data folders and the Windows install folder, Run value, logon task and
uninstall key keep the Firmary Link names (`brand.InstallName`). The Linux package is `firmary-bridge` from 0.3.5; it
replaces the `firmary-link` package, so installing it over one upgrades it. The updater keeps new versions in the
data folder and the copy installed first reads it to find them, so these names can't move without stranding an
install. Installing 0.3.2 or later over a Link upgrades it in place: on Windows the installer swaps the Start menu
entry and the name in Apps; on a Mac, once the old "Firmary Link" app is deleted, Firmary Bridge points the login
item at itself. Copies that only update themselves keep the old app and shortcut names on disk until reinstalled;
what they show is Firmary Bridge.

## Status

**Beta, unsigned.** Version 0.3.4 is the latest GitHub release: the first connection after pairing no longer drops
while a provider's status check is slow, and a Claude sign-in started from Firmary finishes with the code Claude's
page shows, pasted into Firmary. 0.3.3 made the server line work on any Linux, systemd or not, and keep running after
pairing. 0.3.2 brought the Firmary Bridge name; 0.3.1 made Enter pairing
code… open the code box on Windows, 0.3.0 brought pairing v2 (Firmary shows the code, the person types it into the
app) and 0.2.0 was the first Firmary Link release. It is not signed by Apple or Microsoft yet: macOS Gatekeeper and
Windows SmartScreen warn the first time it opens. A signed build is coming. Installed copies update themselves from
the release's signed update manifest.

## Download

From the [latest release](https://github.com/Firmary/bridge/releases/latest). The names don't change between
releases, so `https://github.com/Firmary/bridge/releases/latest/download/<file>` always gives the newest:

| File                                                                       | For                                         |
| -------------------------------------------------------------------------- | ------------------------------------------- |
| `Firmary-Bridge.dmg`                                                       | Mac (Apple silicon and Intel), macOS 13+    |
| `Firmary-Bridge-Setup.exe`                                                 | Windows 10 and 11 (x64 and Arm), per user   |
| `firmary-bridge_amd64.deb`, `firmary-bridge_arm64.deb`                     | Linux desktop (Debian, Ubuntu)              |
| `firmary-bridge_<version>_amd64.deb`, `firmary-bridge_<version>_arm64.deb` | The same two, with the version in the name  |
| `install.sh`, `firmary-link-linux-x64.tar.gz`, `-linux-arm64.tar.gz`       | Linux server or VPS                         |
| `manifest.json`, `manifest.json.sig`, `checksums.txt`                      | The signed update manifest and the SHA-256s |

The Linux packages carry the Firmary Bridge name from 0.3.5; 0.3.4 and before named them `firmary-link_*.deb`. 0.3.5
also carries `firmary-link_amd64.deb` and `firmary-link_arm64.deb`, so links to the old names keep working while
firmary.app moves to the new ones. The `Firmary-Link.dmg` and `Firmary-Link-Setup.exe` copies stopped with 0.3.4;
older releases keep theirs.

- **Mac**: open the dmg, drag **Firmary Bridge** to **Applications** and open it. The first time macOS blocks it: open
  **System Settings › Privacy & Security** and click **Open Anyway**.
- **Windows**: run `Firmary-Bridge-Setup.exe`; at "Windows protected your PC" click **More info › Run anyway**.
- **Linux server or VPS** (no browser needed on the server): Firmary's Settings › Connections › Add a server shows
  one line with the pairing code in it:

  ```sh
  curl -fsSL https://github.com/Firmary/bridge/releases/latest/download/install.sh | sh -s -- --code ABCD-EFGH
  ```

  It installs `firmary-link` for the current user (no sudo) after checking it against `checksums.txt`, makes it
  start with the server and keep running after logout (`firmary-link service install`: a systemd user unit with
  `loginctl enable-linger`, or a background process where systemd doesn't run), pairs it with that code (the
  server prints the code; the person confirms in Firmary) and sends the person back to Firmary, which shows the
  Claude or ChatGPT sign-in. It works on any 64-bit Linux, systemd or not: Docker, WSL and small VPS images too. If
  the service can't be set up at all, it still pairs and then runs in that window. Later:
  `firmary-link status`, `firmary-link pair --code ABCD-EFGH`, `firmary-link unpair`,
  `firmary-link sign-in claude|chatgpt --device`, `firmary-link service status`.

A `vX.Y.Z` tag (plain semver, 0.2.0 or later) on a commit on `main` builds and publishes the release
(`.github/workflows/release.yml`, GitHub-hosted runners only); a weekly run from `main` re-signs the latest manifest
with a fresh expiry, only once its existing signature verifies. A manifest may offer a download only from the
release of its own version (`https://github.com/Firmary/bridge/releases/download/vX.Y.Z/`); the Link refuses any
other.

## Where this comes from

- Design: design studio `5877e3252806` ("local bridge" walkthrough, from chat #1825). The screens this repo serves
  are "Always-on server" (the only terminal people ever see), "Firmary Link on this computer" (restart, what the
  firm sees, permissions; the design still calls it "Helper", the beta's old name, and the app is now Firmary
  Bridge) and "On the computer" (installers on macOS, Windows and Ubuntu).
- Plan: `reviews/personal-ai-relay-build-plan-2026-11-01.md` (decisions G1–G11, slices S0–S18). This repo is the
  plan's Helper repo ("H").
- History: the S2 skeleton was built and security-reviewed in a local `firmary-helper` repo
  (`reviews/firmary-helper-s2-security-review-2026-11-01.md`); its commits are kept here as they were.
- Provenance: all the Go code is new, written for this project (first in that local repo, from 2026-11-01). Nothing
  is copied from another project. The only copied files are data: the relay test vectors, fixtures and JSON
  Schemas in `internal/protocol/testdata` (from Firmary's own `firmary-os`, `packages/validators/ai-relay/helper`),
  Anthropic's public release-signing key `internal/driver/claudecode/anthropic-release-key.asc` and the checksums
  in its `pin.go` (from Anthropic's signed release manifest), and the checksums in `internal/driver/codex/pin.go`
  (GitHub's published digests of OpenAI's Codex `rust-v0.166.0` release archives, and of the executables inside
  them). Go dependencies are BSD-licensed (`gorilla/websocket`
  BSD-2-Clause; `ProtonMail/go-crypto`, `cloudflare/circl`, `golang.org/x/crypto`, `golang.org/x/sys`
  BSD-3-Clause), which Apache-2.0 allows.

## What works

- **Device key.** An Ed25519 key, created on first run in a `0600` file under the data directory. No Firmary secret
  is ever stored on disk.
- **Pairing with the code Firmary shows (v2, 0.3.0).** Firmary shows an `ABCD-EFGH` code (Crockford base 32, ten
  minutes, single use). The person types it into the Link (the app's code box, or `firmary-link pair`, which asks on
  a terminal) or it rides in the server's install line (`--code`). The Link normalises it (upper case, spaces and
  dashes dropped, I and L read as 1, O as 0), connects (commit-then-reveal as before) and sends it in
  `pairing_reveal`. The relay answers `pairing_claimed` with who asked for the code, and the Link shows "Connecting
  to Firmary as <person> · <firm>", the code, "Confirm in your browser if this matches", and Cancel (which closes
  the connection and forgets the code). A refused code (`pairing_code_not_found`, `pairing_code_expired`) is
  forgotten and the Link asks for a new one. Unpaired and without a code, the Link doesn't connect. It never shows a
  code of its own; the v1 code match stays in `internal/relay` for the relay's older Links.
- **Accept on the computer.** Confirming in Firmary makes the relay offer the binding. When the person typed the
  code here and the offer names the same person and firm as `pairing_claimed`, the Link accepts on its own: typing
  the code was this computer's yes (M3). Otherwise it names the person, their email and the firm, and pairs only
  when the person says yes (typing `yes` in a terminal, or the app's menu or dialog); anything else sends
  `pairing_decline` and the Link starts over with a new key. A service with no terminal neither accepts nor
  declines. Unpair (the app's menu, or `firmary-link unpair`) deletes the pairing and the device key here.
- **One outgoing connection.** A `wss://` WebSocket to the relay with reconnect and backoff, plus `hello`. Nothing
  listens on the computer and no ports are opened, the desktop app included.
- **The desktop app.** With a display (macOS, Windows, a Linux desktop with a tray), `firmary-link` with no command
  is a menu-bar or tray app with no terminal: Enter pairing code… (a native box: macOS dialog, a Windows dialog
  the Link draws with Windows' own controls, zenity or kdialog on Linux; it opens by itself when the Link isn't
  paired), the status (Not paired, Connecting to
  Firmary as <person> · <firm> with the code and Cancel, Paired to <person> · <firm>, Offline), Unpair this
  computer, Sign in to Claude, Sign in to ChatGPT, Open at login, Check for updates, Quit. At login it starts
  quietly: the icon appears, it re-checks the pairing, and nothing opens unless the Link needs the person. Without a
  display, or with `--headless`, it is the terminal Link.
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
- **`hello`.** Reports `kind` (`desktop` for the app, `server` for the always-on service or any run without a
  desktop session; also in `pairing_reveal.device`) and both provider apps: `signedIn`, `accountType` (Claude: the
  Claude plan; Codex: the ChatGPT plan type), `orgId` (the Claude organisation or the ChatGPT workspace),
  `accountEmail` (from `claude auth status` or the ChatGPT id token; for the person's own Connections page, never
  logged, omitted when unknown) and `pinnedVersion`, and is sent again after a sign-in or sign-out. The status
  checks run side by side and hello waits for them at most 5 seconds (the relay allows 30): a check still running
  is reported as its last result (signed out if none), and a fresh hello follows once it finishes with a different
  answer.
- **Sign-in from Firmary.** On the relay's `signin`, the Link runs that provider's sign-in in the paired person's
  profile and sends `signin_step` with the link (and code) for Firmary to show: the browser on a desktop, the
  device link on a server (Codex). Claude Code's link ends on a code Anthropic's page shows (it prints no other
  link), so its step has `pasteCode` (kind `paste_code` on a server): the person pastes the code into Firmary, the
  relay's `signin_code` brings it here, the Link writes it to Claude Code, and `signin_result` says at once whether
  it signed in (`signin_code_refused`, `signin_code_invalid`, `signin_not_running` otherwise). On a desktop, signing
  in on the page Claude Code opened there needs no code. On Windows, Claude Code opens that page with
  `rundll32 url,OpenURL`, found on the `PATH` the Link passes through.
- **Silent self-update.** At start and every few hours, between turns only, the Link reads the signed
  `manifest.json` (firmary-os `UpdateManifest`, detached Ed25519 signature, checked against two pinned update keys,
  current and next). A tampered, foreign-key, replayed or expired manifest is refused. A newer Link in this device's
  rollout downloads, is checked against its SHA-256, passes a self-test and is switched in; if it keeps failing to
  start the Link goes back to the previous version. New provider pins move the pinned Claude Code and Codex the same
  way. Nobody sees anything unless something fails.
- **Always on.** `firmary-link service install|uninstall|status`: a systemd unit (user-level when not root, with
  restart and `MemoryMax`), a launchd agent on macOS, a logon task on Windows. On Linux without a running systemd
  (a container, WSL, a small VPS image, or a user without a systemd session) it is a background process instead: a
  small supervisor (`firmary-link supervise DIR`), detached from the terminal, runs the Link and starts it again
  whenever it stops, and an `@reboot` crontab line starts it when the server restarts if cron runs there (if not,
  `service install` says how to start it again). Its record, lock and output are `background.json`,
  `background.lock` and `background.log` in the data directory. `scripts/test-install-no-systemd.sh` runs the
  install line in a clean Ubuntu container against the fake relay. `firmary-link open-at-login on|off` for the
  desktop app.
- **The terminal screen.** The design's "Always-on server" transcript, plus who asked for the code. The copy
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
pairing code, the person's and firm's names, the provider account email, environment variables, raw provider error
text, and the stdout or stderr of `claude`.
The logger has no free-text field, and tests enforce this.

## Layout

| Path                         | What                                                         |
| ---------------------------- | ------------------------------------------------------------ |
| `cmd/firmary-link`           | The Link: desktop app, terminal and commands                 |
| `cmd/link-release`           | Release tool: update keys, writes and signs the manifest     |
| `cmd/fake-relay`             | A local fake relay for development (loopback only)           |
| `internal/brand`             | The product name, and the install names that stay put        |
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
go run ./cmd/fake-relay            # prints a ws:// URL, a ticket-key file and a pairing code
go run ./cmd/firmary-link pair --code CODE --env local --relay-url ws://127.0.0.1:PORT/ --ticket-key-file PATH --data-dir ~/.firmary-link-dev
go run ./cmd/firmary-link run --env local --relay-url ws://127.0.0.1:PORT/ --ticket-key-file PATH --data-dir ~/.firmary-link-dev
```

The first `run` downloads the pinned Claude Code (about 290 MB). The real-Claude-Code tests are opt-in: set
`FIRMARY_HELPER_REAL_CLAUDE` to an install directory. The real-Codex tests (about 120 MB) are opt-in the same way
with `FIRMARY_HELPER_REAL_CODEX`.

CI (`.github/workflows/ci.yml`) runs gofmt, vet, the race tests, the tests on Windows (the code box opens on the
runner's desktop), and builds for macOS (with cgo, on a hosted Mac), Windows and Linux on GitHub-hosted runners.

The Windows executable's manifest (Common Controls 6, per-monitor DPI), version details and icon come from
`cmd/firmary-link/winres/winres.json`, compiled into the committed `rsrc_windows_*.syso` files that `go build`
links in. Its version details (Firmary Bridge, what Task Manager shows) carry 0.0.0.0 in the committed files; the
release build regenerates them with the release version. After changing it or the icon, run in `cmd/firmary-link`:
`go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64,arm64`.

## Licence

Apache-2.0. See `LICENSE` and `NOTICE`.
