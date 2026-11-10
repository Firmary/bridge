# Schema changes needed in firmary-os (`packages/validators/src/ai-relay`)

The Helper commits that fix the S2 security review (2026-11-01) change the wire contract. This file lists the exact
changes the S1 Zod sources need, and the server-side rules S3 must implement. It is checked against
`origin/dev` of firmary-os (S1 contracts, #6970). The JSON Schemas in `packages/validators/ai-relay/helper/` are
regenerated from Zod (`pnpm --filter @firmary/validators run ai-relay:generate`), and fixtures need matching
`valid`/`invalid` cases.

The firmary-os files are **not** edited from this repo; another task is reconciling them.

Review items covered: 2 (pairing), 4 (replay after restart), 5 (org binding), 6 (managed settings), 12 (canonical
relay URL).

---

## 1. Canonical relay URL and the auth payload (review 12) — `core.ts`

The device signs the relay URL as an **exact string**, so the canonical form must be written down once.

```ts
/**
 * A relay URL in canonical form, exactly as the Helper signs it in the auth payload: `wss`, lower-case host, no
 * userinfo, no port unless it is not 443, no trailing dot, path exactly "/", no query or fragment.
 */
export const RelayUrl = z
  .string()
  .regex(/^wss:\/\/[a-z0-9-]+(?:\.[a-z0-9-]+)*(?::(?!443\/)\d{1,5})?\/$/)
  .max(255);

/** The relay of each Firmary environment, byte for byte as the Helper pins it. */
export const RELAY_URLS = {
  prod: "wss://relay.prod.firmary.app/",
  dev: "wss://relay.dev.firmary.app/",
} as const;

/** Prefix of the challenge signature payload. */
export const HELPER_AUTH_CONTEXT = "firmary-helper-auth-v1";
```

If the RE2 generator rejects the `(?!...)` lookahead, drop it and rely on the documented rule. The Helper
enforces the full rule in `relay.CheckURL`.

Auth payload, signed with Ed25519 by the device key (unchanged, now documented):

```
"firmary-helper-auth-v1" + "\n" + <canonical relay URL> + "\n" + base64url_nopad(challenge nonce)
```

**Server rule:** the relay verifies the signature against its **configured constant** (`RELAY_URLS[env]`), never
a URL rebuilt from the `Host` header or the request line.

## 2. Handshake and pairing messages (review 2) — `relay-messages.ts`

Today `relay-messages.ts` says the device-key challenge "is defined with the relay service (S3), not here". The
Helper parses these messages, so they belong in the Helper export. Add the following schemas and union members.

```ts
/** Unpadded base64url of exactly 32 bytes (43 characters). */
const B64Url32 = z.string().regex(/^[A-Za-z0-9_-]{43}$/);
/** Unpadded base64url of exactly 64 bytes (86 characters): an Ed25519 signature. */
const B64Url64 = z.string().regex(/^[A-Za-z0-9_-]{86}$/);

// Relay → Helper. First message on every connection. 32 to 256 random bytes; the Helper refuses anything else.
export const RelayChallenge = z.looseObject({
  type: z.literal("challenge"),
  nonce: z.string().regex(/^[A-Za-z0-9_-]{43,342}$/),
});

// Helper → relay. Answers the challenge.
export const HelperAuth = z.looseObject({
  type: z.literal("auth"),
  publicKey: B64Url32,            // Ed25519 public key
  signature: B64Url64,            // over the auth payload in §1
  helperVersion: Version,
  /** SHA-256 commitment to this connection's helper nonce Nh (§2.1). Sent on every connection. */
  pairingCommit: B64Url32,
});

// Relay → Helper. The key is not paired. Sent only after a valid `auth` (which carries the commitment).
export const RelayPairingPending = z.looseObject({
  type: z.literal("pairing_pending"),
  /** The relay's pairing nonce Nr: exactly 32 fresh random bytes. */
  pairingNonce: B64Url32,
});

// Helper → relay. Reveals Nh, only after it has received `pairingNonce`.
export const HelperPairingReveal = z.looseObject({
  type: z.literal("pairing_reveal"),
  nonce: B64Url32,
});

// Relay → Helper. The person confirmed the code; binds the key to them.
export const RelayPaired = z.looseObject({
  type: z.literal("paired"),
  deviceId: DeviceId,
  userId: UserId,
  firmId: FirmId,
  /** The person's display name (see §6). The Helper prints "Paired to <userName> · <firmName>". */
  userName: z.string().min(1).max(200),
  /** The firm's display name. */
  firmName: z.string().min(1).max(200),
});

// Relay → Helper. The key is already paired.
export const RelayWelcome = z.looseObject({
  type: z.literal("welcome"),
  deviceId: DeviceId,
  userId: UserId,
  firmId: FirmId,
});

// Relay → Helper. A refusal; the Helper ends the connection and reconnects with backoff.
export const RelayError = z.looseObject({ type: z.literal("error"), code: MachineName });
```

Union changes:

- `HelperToRelayMessage`: add `HelperAuth` and `HelperPairingReveal`.
- `RelayToHelperMessage`: add `RelayChallenge`, `RelayPairingPending`, `RelayPaired`, `RelayWelcome` and
  `RelayError`.

### 2.1 Derivations (S3 must compute the same bytes)

All three inputs are exactly 32 bytes, so the concatenations are unambiguous. `||` is byte concatenation and
`0x00` is one zero byte.

```
pairingCommit = SHA-256("firmary-helper-pair-commit-v1" || 0x00 || pub || Nh)
code          = Crockford-base32(SHA-256("firmary-helper-pair-v2" || 0x00 || pub || Nr || Nh)[0:5])
                shown as XXXX-XXXX, alphabet 0123456789ABCDEFGHJKMNPQRSTVWXYZ
```

Test vector, checked against both the Go code and an independent Python implementation:

| Input | Value (unpadded base64url) |
| ----- | -------------------------- |
| `pub` (32 × `0x11`) | `ERERERERERERERERERERERERERERERERERERERERERE` |
| `Nh` (32 × `0x22`) | `IiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiIiI` |
| `Nr` (32 × `0x33`) | `MzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzMzM` |
| `pairingCommit` | `uafKaukt90Y_UQFyZGVVcsEPnDaLe5WwDkNhf8GU724` |
| `code` | `WFWC-XCK4` |

The old derivation `SHA-256("firmary-helper-pair-v1" || 0 || pub || challenge nonce)` is gone. The context string
changed to `-v2`, so an old implementation can't produce a matching code by accident.

### 2.2 Server rules for S3 (pairing)

Ordering is what makes the code safe, so the relay must enforce it.

1. Send `pairing_pending` only after verifying `auth`, which carries `pairingCommit`. Never send `pairingNonce`
   before the commitment.
2. Generate a fresh 32-byte `Nr` for each pending connection.
3. Accept exactly one `pairing_reveal` per connection, and only after `pairing_pending`. Check
   `SHA-256(commit-ctx || 0 || pub || Nh) == pairingCommit`. On a mismatch, send `error` and close.
4. The pending code lives only as long as that connection, and at most 10 minutes. The Helper picks a new `Nh` on
   every connection, so the code changes on reconnect by design: a revealed `Nh` must never be reused.
5. Confirming is single use. Rate-limit confirmations per account and globally, and never log the code.
6. On the confirm screen, show what the server knows about the device: OS, approximate location, time.
7. Send `paired` with `userName` and `firmName` (§6).
8. The Helper's checks, for reference:
   - it refuses a second `pairing_pending` on the same connection;
   - it refuses an `Nr` that is not exactly 32 bytes;
   - it refuses `paired` before it has shown a code;
   - it refuses a `paired` whose `userName` or `firmName` is empty or longer than 200 characters after removing
     control and bidi characters.

   A `welcome` naming a pairing the Helper didn't see confirmed still makes it wipe its key and pair again.

## 3. Provider status in `hello` (review 5 and 6) — `HelperProviderStatus`

```ts
export const HelperProviderStatus = z.looseObject({
  // ... existing fields ...
  /** The provider organisation id (Anthropic org UUID) of the signed-in account, when it has one. */
  orgId: z.uuid().optional(),
  /**
   * True when machine-wide managed settings for the provider were found: IT policy (hooks, env such as a gateway
   * base URL, apiKeyHelper) applies to turns. False means the Helper found none. Firm policy (S11) decides
   * whether managed devices may run turns.
   */
  managed: z.boolean(),
});
```

- `managed` is required, so a missing value never reads as "not managed". The Helper always sends it.
- Detection today:
  - It checks the directories Claude Code reads managed settings from: `/etc/claude-code`,
    `/Library/Application Support/ClaudeCode` and `%ProgramFiles%\ClaudeCode`.
  - Any entry, or a directory that can't be read, counts as managed.
  - Not detected yet: the Windows `HKLM`/`HKCU\SOFTWARE\Policies\ClaudeCode` registry keys, macOS MDM profiles,
    and server-pushed managed settings. These follow in S11.
- **Decided (#1888):** `org`, the organisation's display name, is no longer sent. The Helper doesn't even read
  `orgName` from `claude auth status`. `orgId` and `managed` stay. The S1 schema now refuses a hello that carries
  `org` (`org: z.never().optional()`), so the relay can't store it.

## 4. Ticket claims (review 4 and 5) — `ticket.ts`

```ts
export const TicketClaims = z.looseObject({
  // ... existing fields ...
  /**
   * When present, the turn may run only on a provider login whose organisation id (hello `orgId`) is in this list.
   * Absent: no organisation constraint (account type rules only).
   */
  providerOrgIds: z.array(z.uuid()).min(1).max(16).optional(),
});
```

- **Helper semantics:**
  - Present means the signed-in `orgId` must be in the list, else `policy` / `provider_org_not_allowed`.
  - A signed-in account with no org id never matches.
  - The Helper treats an explicit `[]` as "none allowed". The schema forbids sending it.
- **Decided (#1888):** D7's "business only" means the firm's own provider organisation, not any company's
  Team/Enterprise plan. The business plan types are `team` and `enterprise` (`BUSINESS_ACCOUNT_TYPES` in S1,
  `protocol.BusinessAccountTypes` here). The Codex driver adds its names in S9.
  - The server rule: a ticket whose `accountTypes` allows a business plan type carries `providerOrgIds` with the
    firm's own organisation ids. A firm with none on record gets no such ticket. A ticket for personal plans only
    (a firm that allows them) omits the claim.
  - The Helper rule: it refuses a signed-in business-plan login with `policy` / `provider_org_not_allowed` when
    the ticket has no `providerOrgIds`. A personal-plan login with no claim still runs.
- **`iat` rule (verifier, not schema):** the Helper refuses a ticket whose `iat` is earlier than the whole second
  its process started, with `policy` / `ticket_predates_start`. Update the comment on `TicketClaims` to say so.
  Used ticket ids are also kept on disk, so a ticket issued ahead of the Link's clock and used just before a restart
  is still `ticket_replayed` after it. If that record can't be read, tickets issued before the start plus 30 seconds
  of clock skew get `ticket_predates_start`; if a use can't be recorded, the ticket is refused with `policy` / `ticket_unrecorded`.

## 5. New error patterns the server must handle

These patterns arrive in `error` stream events. All are `MachineName`, so no schema change is needed, but S3's
handling must know them.

| Class | Pattern | Server action |
| ----- | ------- | ------------- |
| `policy` | `ticket_predates_start` | Re-issue the ticket with a fresh `iat`. After any new `hello`, re-issue pending tickets rather than resending them. |
| `policy` | `ticket_unrecorded` | Don't retry. The Link couldn't record the ticket as used (its data directory isn't writable). |
| `policy` | `provider_org_not_allowed` | Don't retry. Tell the person their Claude login is in another organisation, or is a business plan with no firm organisation on the ticket. |
| `transient` | `turn_timeout` | The turn ran past the Helper's 15-minute deadline. Don't auto-retry a turn whose output had started. |

Every rejection uses up the ticket, because `Verify` records the id before the device, prompt and status checks.
That includes `busy` and `status_failed`. A retry always needs a new ticket.

## 6. The person's name in `paired` (Daniel, #2038)

"Paired to …" names the person as well as the firm, as the design does ("Paired to Maria Okafor · Harlow & Pike
CPA"). A Helper serves one person's own AI plan (personal AI only for now), so the person is who the device is
bound to, and the name is what lets them notice a pairing to the wrong account.

- **Schema (`relay-messages.ts`):** add `userName` to `RelayPaired` as in §2: required, 1 to 200 characters, under
  the same rule as the `firmName` comment on `dev` (send it already sanitized, so the Helper's cleanup doesn't change
  it). It is the staff user's display name, not their email.
- **Fixtures:** this repo's copy of `relay-to-helper.fixtures.json` already has it: `userName` in the valid
  `paired`, and two new invalid cases, "paired without the person's name" and "paired with an empty person's name".
  Copy the file back to `packages/validators/ai-relay/helper/fixtures/` when the schema changes.
- **Relay (`apps/worker/src/ai-relay/session.ts`):** the `paired` it sends on `dev` has no `userName`. The Helper
  refuses that `paired` (`bad_paired`) and pairs again on every connection, so the relay must send the name before
  it is connected to this Helper.
- **Never logged:** the Helper shows both names only on a terminal and never writes them to its log or pairing
  file; the end-to-end never-log test checks this. The relay should not log the name either.

## 7. Pairing screen and where the code is shown (bridge PR #1 review)

These are Helper-side only, with no schema change. They set what Firmary's confirm screen (S3/S11) must match.

- **No code on the install line.** The design's server line (`curl … | sh -s HTRW-KQPN`) carries a code Firmary
  made. That can't work with §2.1, because the code exists only once the Helper has connected, and a code on a
  command line would land in shell history and process lists. The one-line install (S10) carries no code.
- **Compare, don't type.** Firmary shows the pending device (OS, approximate location, time; §2.2 rule 6) and the
  code it computed. The person checks that it matches the Helper's code and chooses Pair, or "It doesn't match".
  The code is never typed into Firmary.
- **New code per connection.** Firmary shows the code for the connection that is waiting now; after a reconnect the
  Helper prints "The connection restarted, so the code changed".
- **Terminal only.** The Helper prints the code (and the names in "Paired to") only when stdout is a terminal.
  Under systemd stdout is journald, which other admins can read, so it prints a hint instead. Showing the code to a
  headless service (for example a `0600` file plus a `pairing-code` command) is part of S10.

## 8. Launch profiles (slice L2, design `relay-launch-profile-design-2026-11-02.md`)

The contracts are firmary-os #7026 (L1): `launch-profile.ts`, `turn.profile`, `hello.launch`, and the ticket's
`profileHash` and `profileVersion`. The Helper implements them as written there, with these Helper-side rules that
L3 (the server side) must match:

- **Required.** The Helper refuses a ticket without `profileHash` or `profileVersion` (`ticket_missing_claims`) and a
  turn without `profile` (`profile_invalid`). L1 has them optional in Zod only until L3 signs and sends them.
- **Order of checks.** The Helper hashes `turn.profile` as received (`protocol.ProfileHash`, the shared vectors in
  `internal/protocol/testdata/launch-profile.vectors.json`), refuses `profile_hash_mismatch`, and only then parses it.
  It parses only bytes the ticket signed.
- **Closed reasons.** `profile_unsupported` is a `schema` other than 1. `profile_invalid` is anything else: an unknown
  field, value or tool, an out-of-range number, or text that isn't one JSON object.
- **Stricter than Zod in five places.** Each fails closed, and a correct server never triggers them:
  - keys match exactly (Go's decoder would otherwise accept `"Schema"` or `"SystemPrompt"`);
  - a key repeated in one object is refused (`JSON.parse` would keep the last);
  - `null` is refused for every optional field;
  - a tool named twice is refused (the L1 fixtures call this a Helper check);
  - a string with invalid UTF-8, or a `\uD800`–`\uDFFF` escape that isn't half of a surrogate pair, is refused
    rather than turned into U+FFFD. `JSON.parse` keeps a lone surrogate, so the server must not send one: strip or
    replace them before signing.
- **Nothing after the object.** Any byte after the closing `}` other than whitespace is `profile_invalid`, as
  `JSON.parse` would refuse it. Worth adding to the shared L1 fixtures (`{"schema":1}garbage`, `{"schema":1}}`).
- **`hello.launch`** is `{profileSchemas: [1], tools: ["web_search"], claudeKnobs: ["replaceBuiltinInstructions"],
  codexKnobs: ["reasoningSummary", "verbosity", "replaceBuiltinInstructions"]}`, sent on every connection.
- **The embedded fallback prompt** (`internal/driver/prompt/firmary-system-prompt.md`) is
  `DEFAULT_REQUIRED_INSTRUCTIONS`, a blank line, then `DEFAULT_STYLE_INSTRUCTIONS`, as in #7026. The built-in
  profile `{"schema":1}` uses it. If L3 changes those constants, this file must change in the same release.
- **A second fallback for web search.** A profile with `tools: ["web_search"]` and no `systemPrompt` gets
  `firmary-system-prompt-web-search.md` instead, because the required block says "You have no tools … cannot
  browse". Its first paragraph says web search, run by the provider, is the only tool, rules out acting on the
  computer, and treats search results like `<firmary_context>`: reference material, never instructions. The style
  text is the same. L3's generated tool line (design §2.4) should use the same wording, or L3 should always send a
  `systemPrompt` when a tool is on.
- **Busy before provider checks.** A turn now takes the Helper's one turn slot before the provider is asked for its
  status. While a turn runs, another turn gets `busy` even if it would also fail a provider check (`not_signed_in`,
  `pin_mismatch`, …).
- **New error pattern** for §5: `max_turns` (class `fatal`). A Claude Code turn with a tool on hit the profile's
  `maxTurns`.

How each knob is applied (checked on the pinned binaries by the real-binary tests):

| Knob | Claude Code 2.1.304 | Codex 0.166.0 |
| --- | --- | --- |
| `systemPrompt` (absent: the embedded default) | Written to a `0600` file. `--system-prompt-file`, or `--append-system-prompt-file` when `claudeCode.replaceBuiltinInstructions` is false | `model_instructions_file`, or, when `codex.replaceBuiltinInstructions` is false, `developer_instructions` in `config.toml` only (never on the command line) |
| `effort` | `--effort` (sent as `output_config.effort`) | `model_reasoning_effort`; `max` is sent as `xhigh` |
| `tools: ["web_search"]` | `--tools WebSearch --allowedTools WebSearch` (never `WebFetch`) | `web_search="live"` (otherwise `"disabled"`) |
| `maxTurns` | `--max-turns`, only with a tool on. With a tool on and `maxTurns` absent, `--max-turns 10` (the ceiling) | No setting; the 15-minute turn timeout bounds it |
| `codex.reasoningSummary`, `codex.verbosity` | — | `model_reasoning_summary`, `model_verbosity` |

**What `hello.launch` doesn't say.** It lists what this build parses, the same for both drivers. The wire format stays
as in #7026, so L3's preview must show these three mappings itself (the driver tests pin each one):

- On Codex, effort `max` runs as `xhigh`, Codex's highest level.
- On Codex, `maxTurns` is ignored; a Codex turn with web search is bounded only by the turn timeout.
- Each driver ignores the other provider's knob block (`claudeCode` on Codex, `codex` on Claude Code).

## 9. Firmary Link 0.2.0 (rename, Codex, update manifest)

No message, field or context string changed: the wire contract keeps its `helper` spelling (`helperVersion`,
`firmary-helper-auth-v1`, the pairing contexts). What the server sees from a 0.2.0 Link:

- `hello.providers` lists both `claude-code` and `codex`, and `hello` carries the optional `reconnectReason`
  (`start`, `network`, `update`) and `updateFailure` (`{version, pattern}`, once, after a rolled-back update;
  patterns `self_test_failed`, `start_failed`, `checksum_mismatch`, `restart_failed`) the schema already has.
- A ticket's `adapter` picks the provider app: `helper-claude-code` runs on Claude Code, `helper-codex` on Codex. The
  Codex driver reports its ChatGPT plan type as `accountType` and the ChatGPT account (workspace) id as `orgId`; the
  business plan types per provider match `BUSINESS_ACCOUNT_TYPES` in `devices.ts`.
- `pairing_reveal` now carries the optional `device` the schema already has (`HelperPairingDevice`): `name` (macOS
  ComputerName, Windows COMPUTERNAME, otherwise the hostname; control characters dropped, spaces collapsed, at most 64
  UTF-16 units, left out when empty), `os` and `arch`. It is sent only when pairing, so a computer paired before
  this shows no name until it pairs again.
- New `error` pattern: `policy` / `adapter_disabled`, when the signed update manifest's `disabledAdapters` pauses
  the ticket's adapter (a Link that was offline when the server-side kill switch flipped). Don't retry.
- The update manifest (`update-manifest.ts`) is published by this repo's release workflow for now, as a release
  asset (`https://github.com/Firmary/bridge/releases/latest/download/manifest.json` and `.sig`, a base64 Ed25519
  signature over the exact bytes). One addition, allowed by the open objects: a Codex platform entry carries
  `archiveSha256`, the release archive's checksum, next to `sha256` (the executable's), because the Link verifies
  both. A firmary-os manifest publisher should emit it too.

## 10. Firmary tools (P17)

The launch catalog gains `firmary`: Firmary's own MCP server, called by the turn's Claude Code or Codex with a
short-lived token the server sends with the turn. Firmary enforces every permission and approval on its side
(`confirmation_required`, then the same tool again with `confirmationId` and `waitSeconds`); the Link has no approval
logic of its own.

- **`hello.launch.tools`** is now `["web_search", "firmary"]`.
- **`turn.firmary`** (optional, outside the signed profile and the ticket): `{url, accessToken}`, present exactly
  when the profile's `tools` has `firmary`. The Link refuses the turn with `policy` / `firmary_tools_invalid` when the
  profile has the tool and the turn has no valid credential, or the turn has a credential the profile didn't ask for.
  Keys other than `url` and `accessToken` are ignored; a repeated key or `null` is refused. The shared fixtures
  (`relay-to-helper.fixtures.json`: "turn with Firmary tools (P17)" and its two invalid cases) are copied into
  `internal/protocol/testdata`.
  - `accessToken` must match `^fmcp_[A-Za-z0-9_-]{43}$`.
  - `url` must match the contract's `^https?://[A-Za-z0-9.-]+(?::\d{1,5})?/api/mcp$` (at most 300 characters; the
    Link also accepts `[::1]` on a local relay), with the path exactly `/api/mcp`, and no userinfo, query or
    fragment. Stricter than Zod: its origin must belong to the Link's environment. **prod and dev:** `https`,
    host `firmary.app` or a subdomain of it, no port. **local** (the fake relay): `http` or `https` on `127.0.0.1`,
    `localhost` or `[::1]`, any port. Anything else is `firmary_tools_invalid`.
- **New error pattern** for §5: `firmary_tools_invalid` (class `policy`). Don't retry.
- **Never logged or put on a command line:** the token and the endpoint.
- **`maxTurns`.** With `firmary` on, Claude Code gets `--max-turns` (the profile's, else 10), as for web search.
  Codex has no turn limit (§8).
- **The fallback prompt** doesn't mention Firmary's tools. The server always sends a `systemPrompt` with tools on,
  so it is never used for such a turn.

How each provider gets the tools (checked on the pinned binaries by `TestRealFirmaryTools` in each driver, against
a stub provider API and a fake Firmary MCP server):

| | Claude Code 2.1.304 | Codex 0.166.0 |
| --- | --- | --- |
| Server | `--mcp-config <file>`: a `0600` file in the profile, `{"mcpServers":{"firmary":{"type":"http","url":…,"headers":{"Authorization":"Bearer …"}}}}`, removed when the run ends. `--strict-mcp-config` stays, so no other MCP configuration is read | In `config.toml` only: `mcp_servers.firmary.url`, `mcp_servers.firmary.bearer_token_env_var="FIRMARY_MCP_TOKEN"`. The token is set only in that Codex's environment |
| Built-in tools | `--tools` still lists only the catalog's built-ins (`""` for `firmary` alone): it governs built-in tools, not MCP tools | Unchanged locked settings |
| No local prompt | `--allowedTools mcp__firmary` (every tool of that server and nothing else) | `mcp_servers.firmary.default_tools_approval_mode="approve"`: under `approval_policy="never"`, Codex would otherwise refuse any tool not annotated read-only |
| Startup | Waits for the server | `mcp_servers.firmary.required=true`: waits for the server instead of dropping its tools after the default 1-second grace, and fails the turn if it can't connect |
| How the model sees them | `mcp__firmary__<tool>` | Deferred behind Codex's own client-side `tool_search` (always on in 0.166.0), as namespace `mcp__firmary`; Codex also offers its MCP resource tools (`list_mcp_resources`, …), which reach only Firmary's server. Exec's `mcp_tool_call` items for server `firmary` are let through; any other tool item still ends the turn |

## Not covered here

The other task reconciling the schemas will find these older differences between this Helper and S1. They predate
the review fixes and are left to that task:

- tickets: `accountTypes` vs `accountPolicy`, `promptHash` coverage vs `contextHash`/`schemaHash`;
- `turn` carries `turnId` in the Helper but not in `RelayTurn`;
- `accountType` raw plan names vs `personal`/`business`;
- `pinnedVersion` reported as empty vs absent.
