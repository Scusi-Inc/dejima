# Design — multi-tenant island ownership + privacy

**Goal (a3 #90/#92, operator-greenlit, Amanda will dogfood):** make one daemon
safely serve multiple humans. Today team-auth tokens have a role + a *static*
`--island` scope, which breaks for teammates two ways:
1. a scoped token **can't create islands** (create is a global mutation, denied
   in `authorizeRole` for any island-scoped token), and
2. the islands **list/overview aren't filtered** — a scoped token *sees* every
   island's name/details even though it can't act on them.

So a teammate can neither stand up their own islands nor avoid seeing the
operator's. This spec defines the real model: **per-island ownership**, enforced
server-side, with private visibility and a privacy-preserving aggregate.

This is the "multi-user on ONE daemon" story; it supersedes the parked
per-user-daemon idea (roadmap #32).

## The core insight (why static island-scope can't do this)

The existing scope (`authtoken.Identity.Islands`) is a **static list of names**.
Ownership is **dynamic**: "the islands Amanda created" includes islands that
don't exist yet when her token is minted. A static list can't express that. So
multi-tenancy needs a new, orthogonal dimension on the identity — an **owner id**
— and authorization that resolves an island's owner at request time.

Owner-scope and the existing island-scope are **orthogonal and compose**: an
owner-scoped token sees its own islands; adding an explicit `--island` list
further narrows it (e.g. a CI token for one of Amanda's islands).

## Data model

### Identity / token (`internal/authtoken`)
Add an `Owner` field — a stable owner id, set at mint, enforced server-side
(never client-supplied):

```go
type Token struct {
    ID, Label string
    Role      Role
    Owner     string   // NEW: owner id this token acts as ("amanda"); "" = host owner
    Islands   []string // existing static scope (orthogonal, optional)
    Hash      string
    CreatedAt time.Time
}
type Identity struct {
    TokenID, Subject string
    Role             Role
    Owner            string   // NEW: resolved owner id
    Islands          []string
}
```

- `Resolve(secret)` copies `Token.Owner` → `Identity.Owner`.
- **Host owner**: the trusted local-socket caller (`trustedOwner()`,
  `Role=RoleOwner`) and any `RoleOwner` token get the configured **host-owner id
  (default `aoos`)**. `RoleOwner` *sees and manages everything* regardless of
  owner — it's the admin (P3 requirement #3).
- A teammate token is `RoleOperator` (or `viewer`) **with `Owner=<their id>`**.

New helper:
```go
// OwnsAll reports the admin bypass (host owner sees/manages all islands).
func (i Identity) OwnsAll() bool { return i.Role == RoleOwner }
```

### Island (`internal/project`)
`Project.Owner` **already exists** (`project.go:139`, TOML `owner,omitempty`) —
today free-form/informational. We make it **authoritative**: it holds the
owner id of the creating identity. No new field; we change how it's *set* and
that it's *enforced*. (Keep `omitempty` — empty is meaningful pre-migration and
is what the backfill detects.)

## Authorization

Two enforcement points, both server-side:

### 1. Create → stamp owner (P1)
`createIsland` (`server.go:1427`): stamp `p.Owner = identity.Owner` from the
authenticated caller (not `req.Owner` — that field becomes ignored/owner-role-only
to prevent spoofing). An owner-scoped operator token **may create** even though
create is a "global" route, because the result is *its own* island.

Fix in `authorizeRole`: the current block rejects *any* `Scoped()` token from
global non-read routes. Refine so the **create route is allowed for `capOperate`
regardless of owner-scope** (owner-scoped ≠ island-scoped — an owner token has
empty `Islands`, so it already passes the `Scoped()` gate; we just must not add
owner to that gate). Net: owner-scoped tokens reach create; the new island is
attributed to them.

### 2. Touch → owner gate (P1)
For island-scoped routes (`/{name}`), after the role-cap + static-island checks,
require **`identity.OwnsAll() || island.Owner == identity.Owner`**. This needs
the island's owner at auth time, which the auth layer doesn't load today. Inject
a lookup so `authorizeRole` stays decoupled + unit-testable:

```go
// set on the Server; authorizeRole closes over it
ownerOf func(island string) (owner string, ok bool)  // project.Load(name).Owner
```

- Unknown island → fail-closed (404/403 as today).
- `RoleOwner` (host) bypasses → manages all.
- A non-owner teammate touching someone else's island → **403** (same shape as
  the existing out-of-scope error).

This mirrors the spawn-gate pattern (load under the read path, enforce in the
auth layer) and the existing `MayTouch` island check — it's an additional
predicate, not a new middleware.

## Private visibility (P2)

Filter the two fleet-wide reads to the caller's owned islands; `RoleOwner` sees
all:

- `listIslands` (`server.go:912`): after `project.List()`, keep `p` where
  `identity.OwnsAll() || p.Owner == identity.Owner`.
- `handleOverview` (`access.go:200`): same filter before aggregating, so a
  teammate's overview reflects *their* fleet, not the host's.

Edge: islands with `Owner==""` after migration shouldn't exist (see Migration);
if one slips through, only `RoleOwner` sees it (fail-closed to private).

## Aggregate analytics (P3)

A privacy-preserving, host-wide rollup so a teammate can see utilization without
seeing *what* is running. New read route:

```
GET /v1/aggregate  (capRead)  →  {
  total_islands int, running int, hibernated int,
  memory_usage_bytes, memory_limit_bytes, cpu_percent,
  disk_total_bytes int64
}   // counts + totals ONLY — never names, repos, owners, or per-island rows
```

Computed across **all** islands (reusing `statsAll` / `volumeSizes` /
`disk.total_bytes`) regardless of caller owner — that's the point: shared host
utilization, zero specifics. Readable by any authenticated caller. (This is the
shape the operator explicitly asked for.)

## Migration (P1)

Existing islands have `Owner==""`. Stamp them to the host owner (`aoos`) so they
become the operator's and stay private from teammates. Use the **existing
load-time idempotent backfill choke point** (`project.Load`, same pattern as
`BackfillAgentLabels`):

```go
if p.Owner == "" {
    p.Owner = hostOwnerID()   // default "aoos", configurable
    _ = p.Save()              // idempotent: only the first load per island writes
}
```

Passive, no command to run, concurrency-safe (idempotent + atomic save). One-time
cost per legacy island on first read. (Alternative: an explicit
`dejima admin migrate-owner` — heavier, not needed.)

## Touchpoint with the just-shipped team invite (#219)
The invite already carries `role` + `islands`. Add `--owner` to
`dejima token create` / `token invite` (host-owner only may set it) and let the
invite **echo the owner for display** on the join side. The owner is resolved
*server-side from the token* — the invite never grants owner authority by itself.

## Phasing (each its own reviewed, CI-green PR)

- **P1 — Ownership + create + migration** (the foundation):
  `Token.Owner`/`Identity.Owner`; `Resolve` wiring; host-owner-id config (default
  `aoos`); `token create --owner`; stamp `project.Owner` from identity on create;
  allow owner-scoped operators to create; the **owner gate** (`ownerOf` lookup) on
  `/{name}` routes; load-time migration. Tests: create-as-teammate→owned;
  teammate-can't-touch-operator's (403); host-owner manages all; migration stamps
  legacy→aoos; create no longer blocked for owner-scoped.
- **P2 — Private visibility**: filter `listIslands` + `handleOverview`; `RoleOwner`
  sees all. Tests: teammate lists only own; operator lists all.
- **P3 — Aggregate analytics**: `GET /v1/aggregate` (no specifics) + client/SDK +
  openapi + parity. Tests: totals correct, no names leak, any-role readable.
- **P4 — (a2) TUI**: own/all toggle, "your islands" default, aggregate panel —
  after the model lands. a2 owns this.

**Acceptance (operator):** Amanda creates + manages her own islands, cannot
see/touch the operator's; the operator (host owner) sees all + the aggregate.

## Forks to confirm before building

1. **Island name uniqueness** *(recommend: keep globally-unique)*. Names are the
   container/volume/network identity (`dejima-<name>`), globally unique today.
   Per-owner namespacing (Amanda's `webapp` ≠ operator's `webapp`) is stronger
   privacy but invasive (touches every runtime name). Keep global uniqueness;
   mitigate the one privacy leak — a "name taken" error revealing an island the
   teammate can't see — with a **generic "name unavailable"** response. Revisit
   namespacing only if dogfooding demands it.
2. **Owner id source** *(recommend: dedicated `Token.Owner`)* vs reusing
   `Subject`/`Label` (fragile, collides).
3. **Owner enforcement location** *(recommend: `ownerOf` lookup injected into
   `authorizeRole`)* vs a separate middleware vs per-handler (too many handlers).
4. **Aggregate visibility** *(recommend: any authenticated caller)* — that's the
   "see utilization, not specifics" goal; tighten to operator+ only if preferred.
5. **Migration** *(recommend: passive load-time backfill)* vs explicit command.
6. **Local-socket attribution** *(recommend: socket caller = host-owner id `aoos`,
   configurable)* so socket-created islands are owned by the host, not orphaned.

## Containment / honesty
- Owner is **server-authoritative** (from the token record via `Resolve`), never
  client-supplied — a teammate can't claim another owner.
- Default-safe: ownerless/unknown → only `RoleOwner` sees it (fail-closed).
- The host owner (local socket + `RoleOwner` tokens) remains the unattenuated
  admin — this adds a *tenant* layer beneath it, it doesn't weaken the owner.
- This is **co-tenant** isolation (logical: ownership-gated API access on a shared
  daemon), not kernel/container isolation between tenants' islands — say so
  plainly; islands are already container-isolated from each other, and the daemon
  is the trust root either way.
