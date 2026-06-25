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
`{ id, from, from_agent, to, to_agent, topic, action, params?, created_at }`
— requesting island/agent (`from`/`from_agent`) → `action` over the granted
`topic` channel → target island/agent (`to`/`to_agent`), with `params` the payload.

## Contract gaps to resolve with a1 (BEFORE building)
1. **Risk tier is missing from the payload.** The spec's headline UX — "make the
   destructive tier unmistakable; never approve blind" — and tier-colored rows are
   impossible unless each `ActionRequest` carries its computed tier. **Ask:** add
   `risk` (enum `benign|mutating|destructive`) to the GET payload (the gate already
   knows it — that's how it decides auto-approvable vs human-gated).
2. **Policy CRUD endpoints aren't there yet.** The overlay's "active auto-approve
   rules" section, the `[r] approve + rule` action, and the spec's `dejima policy
   add/ls/rm` need: list rules, create rule, delete rule. **Ask:** confirm routes
   (e.g. `GET/POST /v1/link/policy`, `DELETE /v1/link/policy/{id}`) and the rule
   shape (`{link/from→to, action, max, ttl}`).
3. **`approve + rule`** — does `approve` take an inline rule spec, or is it
   `approve` followed by a separate policy-create? (Prefer one call so the UI is
   atomic; otherwise the client does both.)
4. **Deny reason** — confirm the `deny` body (`{reason?}` per the CLI `--reason`).
5. **Push vs poll** — the TUI will **poll** `GET /v1/link/actions` on its existing
   2s tick (cheap, no new infra) to drive the count badge. Confirm that's fine, or
   point me at an event/webhook if live push is preferred.

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
