# Team invite format — v1 spec

The "Team-in-the-TUI" join flow: an **owner** issues an invite in the TUI; a
**teammate** pastes one paste-safe blob and is connected — no env vars, no
manual `--host`/`DEJIMA_TOKEN`. This doc is the **wire contract** so the issue
side (a1) and the join side (a2) can build in parallel.

## 1. Wire format

A single line, ASCII, paste-safe:

```
dejima-invite:<base64url-nopad(JSON)>
```

- Prefix `dejima-invite:` (constant `invite.Scheme`) makes it greppable and lets
  a paste handler recognise it.
- Body is **base64url, no padding** (RFC 4648 §5) of the JSON payload below.
  URL/filename-safe alphabet → survives copy/paste, chat, QR, shell args without
  `+`, `/`, or `=` getting mangled or line-wrapped.
- No encryption — see §5. base64 is encoding, not secrecy. The blob **is** a
  credential.

## 2. JSON payload (`invite.Payload`)

```json
{
  "v": 1,
  "host": "minion.tailabc.ts.net:7274",
  "token": "<bearer secret>",
  "role": "operator",
  "islands": ["webapp"],
  "name": "minion",
  "label": "Amanda"
}
```

| field     | type     | req | meaning |
|-----------|----------|-----|---------|
| `v`       | int      | ✔  | format version; **1**. Decoder rejects unknown majors. |
| `host`    | string   | ✔  | daemon `host:port` the teammate dials. **Operator-supplied** (the daemon can't know its own external address — see §4). |
| `token`   | string   | ✔  | bearer secret from `CreateTokenResponse.Secret`. **This is the credential.** |
| `role`    | string   | ✔  | `owner`\|`operator`\|`viewer` — echo of the minted token's role (display/UX only; the daemon enforces the real scope). |
| `islands` | []string |     | scope echo; empty = all islands. |
| `name`    | string   |     | suggested profile name to save under; join side defaults to the host's hostname if empty. |
| `label`   | string   |     | token label / who it's for (display only). |

Compact-but-readable keys; a typical blob is ~120–180 chars → fits one paste / a
small QR.

## 3. Go API — `internal/invite` (a1 ships this; a2 imports `Decode`)

```go
package invite

const Scheme  = "dejima-invite:"
const Version = 1

type Payload struct {
    V       int      `json:"v"`
    Host    string   `json:"host"`
    Token   string   `json:"token"`
    Role    string   `json:"role"`
    Islands []string `json:"islands,omitempty"`
    Name    string   `json:"name,omitempty"`
    Label   string   `json:"label,omitempty"`
}

// Encode validates required fields, stamps V=Version, marshals, base64url-nopad,
// and prefixes Scheme. Issue side (TUI Team view / `dejima token invite`).
func Encode(p Payload) (string, error)

// Decode trims optional surrounding whitespace, strips Scheme (error if absent),
// base64url-decodes, unmarshals, and validates V==Version + host/token/role
// present + role ∈ {owner,operator,viewer}. Join side.
func Decode(s string) (Payload, error)
```

`Decode` is **strict and total**: every malformed input returns a clear error
(missing prefix, bad base64, bad JSON, wrong version, missing required field,
bad role) — never a panic, never a partial Payload. a2 can render the error
verbatim to the user.

## 4. Issue side (a1)

1. Owner picks role + island scope (+ optional label) in the TUI Team view.
2. TUI calls `POST /v1/tokens` (`CreateTokenRequest{Label, Role, Islands}`,
   owner-only) → `CreateTokenResponse{Token, Secret}`. **The secret is returned
   exactly once** — so `Encode` must run right here, at mint time.
3. **Host:** the daemon cannot reliably self-detect its externally-reachable
   address, so the operator supplies it. The Team view prefills from the active
   profile's host (or a configured advertise address) and lets the operator
   confirm/edit. → `invite.Encode(Payload{Host, Token: secret, Role, Islands,
   Label})`.
4. Show the blob (copy button + QR later). Revocable: `DELETE /v1/tokens/{id}`
   kills a leaked invite.

**Existing token APIs are sufficient** — no new endpoint needed: POST/GET/DELETE
`/v1/tokens` already carry `{label, role, islands}` + return the secret once, and
`ListTokens`/`RevokeToken` back a TUI Team table (mint / list / revoke). a1 adds
only `internal/invite` + persistence (§6), not daemon routes.

## 5. Security (state this in --help and the TUI)

- The invite **contains the bearer secret** (base64-encoded, *not* encrypted) →
  treat it like a password. Send over a trusted channel; anyone with the blob
  has the token's access until it's revoked.
- Mint **narrow** invite tokens: least-privilege role + island scope, one per
  teammate, so revocation is surgical.
- Revoke via `DELETE /v1/tokens/{id}` (kills the token → the invite is dead).
- At rest: stored in `client.json` at `0600` (§6) — same posture as the existing
  per-island token files (`~/.dejima/projects/<n>/token`, 0600). OS keychain is a
  later enhancement, not v1.

## 6. Persistence on join (a1)

Today `clientcfg.Profile` is `{Name, Host}` (host-only; token is env-only). Add:

```go
type Profile struct {
    Name    string   `json:"name"`
    Host    string   `json:"host,omitempty"`
    Token   string   `json:"token,omitempty"`   // NEW: bearer secret for this target
    Role    string   `json:"role,omitempty"`    // NEW: display only
    Islands []string `json:"islands,omitempty"` // NEW: display only
}
```

- New store helper `clientcfg.SaveInvite(p invite.Payload) (profileName string,
  err error)` — upserts a `{Name, Host, Token, Role, Islands}` profile and makes
  it active. a2's join UI calls `invite.Decode` → `clientcfg.SaveInvite` →
  "Connected as <role> to <host>".
- **Token resolution** (`cmd/dejima/main.go`): use the active profile's `Token`
  when set; `DEJIMA_TOKEN` env still overrides for the scripted/in-island path
  (no-profile clients are unaffected). *Precedence is the one open call — env-wins
  vs profile-wins; proposing env-wins-when-set (least change to existing
  behavior), profile-token as the no-env default. a2/a3 confirm.*
- `client.json` is already written `0600`; no perms change.

## 7. Test vectors (a1 provides; a2 can hardcode for the join UI)

- Round-trip: `Decode(Encode(p)) == p` for a full payload and a minimal one
  (host+token+role only).
- Reject: no prefix; bad base64; bad JSON; `v:2`; missing host/token/role; bad
  role — each a distinct, surfaced error.
- A frozen golden blob string both sides assert against (pinned in the PR).
