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
  /** The firm's display name. The Helper prints "Paired to <firmName>". */
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
7. Send `paired` with `firmName`.
8. The Helper's checks, for reference:
   - it refuses a second `pairing_pending` on the same connection;
   - it refuses an `Nr` that is not exactly 32 bytes;
   - it refuses `paired` before it has shown a code;
   - it refuses a `paired` whose `firmName` is empty or longer than 200 characters after removing control and bidi
     characters.

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
- `org` (the display name) is still sent. Whether to drop it in favour of `orgId` is a consent and D7 call
  for Daniel.

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
- What D7's "business only" means, whether that is "any Team/Enterprise plan" or "this firm's organisation", is
  Daniel's decision. This claim makes the second meaning enforceable on the device.
- **`iat` rule (verifier, not schema):** the Helper refuses a ticket whose `iat` is earlier than the whole second
  its process started, with `policy` / `ticket_predates_start`. Update the comment on `TicketClaims` to say so.

## 5. New error patterns the server must handle

These patterns arrive in `error` stream events. All are `MachineName`, so no schema change is needed, but S3's
handling must know them.

| Class | Pattern | Server action |
| ----- | ------- | ------------- |
| `policy` | `ticket_predates_start` | Re-issue the ticket with a fresh `iat`. After any new `hello`, re-issue pending tickets rather than resending them. |
| `policy` | `provider_org_not_allowed` | Don't retry. Tell the person their Claude login is in another organisation. |
| `transient` | `turn_timeout` | The turn ran past the Helper's 15-minute deadline. Don't auto-retry a turn whose output had started. |

Every rejection uses up the ticket, because `Verify` records the id before the device, prompt and status checks.
That includes `busy` and `status_failed`. A retry always needs a new ticket.

## Not covered here

The other task reconciling the schemas will find these older differences between this Helper and S1. They predate
the review fixes and are left to that task:

- tickets: `accountTypes` vs `accountPolicy`, `promptHash` coverage vs `contextHash`/`schemaHash`;
- `turn` carries `turnId` in the Helper but not in `RelayTurn`;
- `accountType` raw plan names vs `personal`/`business`;
- `pinnedVersion` reported as empty vs absent.
