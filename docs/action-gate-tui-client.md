# Action-gate approval client (TUI) — design draft

**Status:** design draft, POST-LAUNCH (a3 brief SEAM #8). Not built — gated on the
backend (a1, branch `feat/link-action-gate`; spec `docs/action-gate-spec.md`).
This pins the TUI client design + the **API contract it needs**, so the backend
can confirm the payload shape before either side commits. The TUI is one of three
front-ends (TUI / CLI / webhook); it owns *no* policy or enforcement — the daemon
gate does (queue + policy + ledger). See `docs/action-gate-spec.md` §"Surfaces".

## What exists today (build against this)
Endpoints on `feat/link-action-gate`:
- `GET  /v1/link/actions` → pending `[]link.ActionRequest`
- `POST /v1/link/actions/{id}/approve`
- `POST /v1/link/actions/{id}/deny`

`link.ActionRequest` (internal/link/action.go):
`{ id, from, from_agent, to, to_agent, topic, action, params?, created_at, tier }`
— requesting island/agent (`from`/`from_agent`) → `action` over the granted
`topic` channel → target island/agent (`to`/`to_agent`), `params` the payload, and
**`tier`** the risk classification (`benign|mutating|destructive`).

## Contract — resolved with a1 (msg #17, slices 1+2 merged: #131 tiers, #132 policy engine)
1. **Risk tier — DONE.** `GET /v1/link/actions` items carry `tier`
   (`benign|mutating|destructive`), classified at request time; same `tier` is on
   the `link.action-pending` event. (Spec calls it "risk tier"; the field is `tier`.)
   **Engine guarantee:** a destructive action is *never* auto-approved
   (`policy.Consume` filters on tier), so it ALWAYS reaches the prompt — which is
   exactly what the "render destructive unmistakable" UX relies on.
2. **Policy CRUD — engine merged (#132, internal/policy); the CRUD API is slice 3
   (next).** Rule shape: `{from, to, topic, action, max_count (0 = unlimited within
   ttl), used, expires_at, created_at, created_by}`. List/create/delete routes land
   in slice 3 (a1 will confirm exact paths).
3. **`approve + rule` — approve THEN a separate policy-create** (composable; a1 may
   add an inline convenience later). Build `[r]` as approve + policy.create.
4. **Deny reason — slice 3.** `POST .../deny` ignores the body today; `{reason}`
   lands in slice 3 (matches the CLI `--reason`). Build the prompt for it.
5. **Count badge — poll is fine.** Poll `GET /v1/link/actions` on the 2s tick. A
   `link.action-pending` push event + a slice-4 SSE (`dejima approvals watch`) exist
   if we want live push later; not needed for the badge.

## Build split (post-launch)
- **Buildable now (slices 1+2):** the announcement-bar count/alert (tier-aware red
  when any destructive pending), the pending queue, and `[a]` approve / `[d]` deny /
  `[v]` view — with tier-colored rows and the **destructive typed-confirm**.
- **Slice 3 (when CRUD + deny-reason land):** the active-rules section, `[r]`
  approve+rule (approve → policy.create), and the deny `{reason}` field.

## Client UI (mirrors patterns already shipped)
Two surfaces, in the **same trust-surface family** as the grants view (`T`, #111)
— one governance mental model, not a bolt-on dialog.

### 1. Announcement-bar alert (reuse `announcement()`, #96)
When pending > 0, a header broadcast (its own slot, slots prioritized): 
- any **destructive** pending → red+bold (`styleErrorBroadcast`): `⚠ N action(s) need approval — [G] review`
- else → amber (`styleBroadcast`): `⬆ N action(s) await approval — [G] review`
Driven by a polled `m.pendingActions` count; sits below PANIC, above the
update/skew banners (a pending destructive cross-island action outranks "update
available"). A count badge, never auto-dismissing while pending > 0.

### 2. Approvals overlay (key `G` = Gate; full-pane, like `renderGrantsView`/`auditView`)
- **Pending** section — each row: `from/from_agent  →  action  →  to/to_agent`,
  a **tier badge** (benign=muted · mutating=amber · destructive=red+bold ⚠), and
  age. Navigable (j/k). Grouped by `link (from→to)` then `action` to fight
  approval fatigue (spec §fatigue).
- **Rules** section — active auto-approve rules (link · action · remaining count ·
  TTL), so the operator sees what's auto-approving (forthcoming endpoint, gap #2).
- Per-pending keys (mirroring the spec exactly):
  - `[a]` approve · `[d]` deny (prompts a reason via the confirm widget) ·
    `[r]` approve **+ rule** (scoped: this link+action, with a count/TTL — a small
    form) · `[v]` view full payload (`params` + the `topic` link grant + tier).
  - **Destructive approve requires a typed confirm** (same widget as purge) so it
    can't be rubber-stamped among benign rows — the spec's anti-fatigue core.
  - Fatigue mitigation v1: tier coloring + grouping; **batch-approve similar**
    (group → approve all) as a fast-follow once single-approve is solid.
- Read-only for viewers (owner/operator approve; spec §RBAC) — the TUI runs as
  operator, so this is mostly moot, but disable the action keys if a viewer token.
- **Fail-safe is server-side:** unapproved-in-window → denied by the daemon; the
  client just reflects the list (the row disappears). The TUI never holds state of
  record.

## Reuse map
- `announcement()` slot + `styleBroadcast`/`styleErrorBroadcast` (#96) — the alert.
- `renderGrantsView`/`auditView` overlay pattern (#111) — the approvals overlay.
- The typed-confirm widget (`confirmPrompt`) — destructive approve + deny reason.
- The 2s poll (`tickMsg` → `fetchListCmd`-style) — add a `fetchPendingActionsCmd`.
- New: `approvalsView` model field; client `ListPendingActions` / `ApproveAction(id, *rule)` / `DenyAction(id, reason)`; `[G]` key + `m.pendingActions`.

## Verification (when wired)
- API: httptest over list/approve/deny (+ policy CRUD) — and the roleauth matrix +
  openapi entries for any new routes (the coverage-gate + parity gates apply).
- TUI: overlay renders a fixtured pending set with correct tier coloring; the
  destructive row demands the typed confirm; the announcement count + red-when-
  destructive logic; viewer sees no action keys.
- Manual (host, multi-island): trigger a cross-island action → it appears pending →
  approve/deny → `dejima audit --verify` shows the `link.*` ledger line.

## Out of scope (per spec)
Destructive auto-approve rules (future, opt-in, non-default). Egress/LLM-API gate
(not covered). Policy *enforcement* (daemon's, not the client's).
