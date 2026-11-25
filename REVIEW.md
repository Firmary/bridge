# Reviewing Firmary Bridge (bridge)

What a reviewer of this repo must know. RoboRev reads this file (its `REVIEW.md` fallback) and OpenQodex quotes it
(`review.instructions_files`). The rules come from README.md (sections named below) and SCHEMA-CHANGES.md; AGENTS.md
holds only the OpenQodex steps agents follow before a push.

## Flag these

### Turns run only on a valid ticket (What works › Turns)
- A turn runs only with a ticket signed by Firmary's pinned key, for this device and its paired user, unexpired,
  unused and matching the prompt (`internal/ticket`, `internal/helper`). A path that starts a provider run without all
  of these checks is a finding. With no pinned key a turn is refused (What is not built yet).
- Firmary's own tools are scoped to that person and that turn and nothing else; their MCP config file is private and
  the turn deletes it.

### The provider apps run locked down (Its own pinned Claude Code / Codex)
- Claude Code and Codex are the Link's own pinned copies, checked against the checksums in `internal/driver/*/pin.go`
  (and Anthropic's signed manifest for Claude Code). A download that skips a check is a finding.
- Each runs in the Link's own profile (`CODEX_HOME`, separate Claude profile) with the updater off, a cleaned
  environment and Firmary's system prompt in place of the app's own. The person's own install and login are never
  touched.
- Codex: no shell, apps, plugins, hooks, skills, memories, project instruction files or saved sessions; no MCP server
  or web search unless the turn's launch profile turns on Firmary's own; `apply_patch` stays in a read-only sandbox
  with approvals off. Claude Code: no tools but those the launch profile turns on. Anything a launch profile sets
  stays under the hard ceiling in `internal/launch`.
- The Link never opens Codex's `auth.json`; account details come from `claude auth status` and Codex's `account/read`.

### Secrets, keys and the network (What works)
- No Firmary secret is stored on disk. The device key is Ed25519 in a `0600` file; `internal/fsperm` checks ownership.
- One outgoing `wss://` connection. Nothing listens on the computer and no port is opened, the desktop app included.
  `cmd/fake-relay` stays loopback only.
- Pairing: codes are single use and ten minutes; a refused code is forgotten; the Link never shows a code of its own;
  it accepts without asking only when the code was typed here and the offer names the same person and firm as
  `pairing_claimed`. A service with no terminal neither accepts nor declines. Unpair deletes the pairing and the key.

### Updates (Silent self-update)
- The manifest's detached Ed25519 signature is checked against the two pinned update keys; tampered, foreign-key,
  replayed or expired manifests are refused. Downloads come only from their own release and are checked against their
  SHA-256 and a self-test before switching; failing starts roll back. Updates run between turns only.

### Logs (What it never logs)
- `internal/logx` logs metadata only and has no free-text field. Prompts, context, outputs, file contents, tokens,
  credentials, sign-in URLs and codes, the pairing code, people's and firms' names, the account email, environment
  variables, raw provider error text and `claude`'s stdout or stderr must never reach a log.

### Names and the wire contract
- The product name lives only in `internal/brand/brand.go`; a test fails if another Go file spells it out.
- Install names (`brand.InstallName`: the command, macOS bundle id, data folders, Windows install folder, Run value,
  logon task, uninstall key) don't move, or installs are stranded.
- Protocol context strings such as `firmary-helper-auth-v1` and every relay message are the wire contract with
  firmary-os's relay. A change to a message shape is listed in SCHEMA-CHANGES.md (the changes firmary-os's Zod
  sources and relay need); firmary-os files are never edited from this repo. `internal/protocol/testdata` holds the
  schemas, fixtures and vectors copied from firmary-os.
- The terminal copy is in `internal/console`, pinned by tests to the design's words.

### Platforms
- Code builds and runs on macOS, Windows and Linux, including Linux without systemd (the background supervisor,
  `scripts/test-install-no-systemd.sh`). Windows must not flash console windows during status checks.

## Do not flag
- Style, naming, comment wording or formatting: CI runs gofmt and vet.
- Missing tests beyond the one focused test a fix needs.
- Hardening for paths nothing reaches, or defensive checks for inputs a trusted caller already validated.
- Speculative abstractions, options or configuration nobody asked for: suggest none.
- Unsigned builds (signing waits for certificates, S15) and the other items in "What is not built yet".
- Generated or copied files: `cmd/firmary-link/rsrc_windows_*.syso`, `internal/protocol/testdata/`, the pinned
  checksums and Anthropic's release key, `go.sum`, `bin/`, `dist/`.
