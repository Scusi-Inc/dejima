# Action Gate — cross-island action approval (Lane 5 P3)

> Status: design spec (2026-06-24). The human-in-the-loop approval gate for
> cross-island actions. Companion to the inter-island exchange work (link broker)
> and the governance/audit story. Builds on: Lane 2 roles (owner/operator/viewer),
> the tamper-evident ledger, and the deny-all brokering model shared with Port
> (host files) and MCP (tools).

## Purpose

When one island (e.g. an orchestrator agent) acts on **another** island over a
granted link, each action passes through a gate that a human operator approves
(or a scoped policy auto-approves). This is the backstop against the
"one rogue agent visits every island and tells them to wipe their drives"
scenario: an orchestrator is **an agent with grants, never an admin**, and every
cross-island action is gated, scoped, and ledgered.

## Model: grant = envelope, gate = each exercise

- **Link grant** (static, set up front via the link broker): island A may talk to
  island B *at all*, scoped to certain action types. Defines the **ceiling** of
  what is even possible.
- **Action gate** (dynamic, per request): each actual action over that link is
  checked → auto-approved (by policy) or human-gated. Governs each **exercise**
  within the ceiling. A grant never pre-authorizes behavior; it only makes it possible.

## Risk tiers

- **Benign / read** (status peek, read a file in granted scope) — auto-approvable.
- **Mutating** (write, dispatch a task, trigger a build) — gated by policy/human.
- **Destructive** (delete, purge, write outside scope, irreversible cross-boundary)
  — **always explicit human approval; never silently auto-approvable.** This is the
  wipe-the-drive backstop.

## Default posture — LOCKED: prompt-everything

Out of the box, **every** gated action prompts a human. The operator opts *into*
automation by adding scoped auto-approve rules as they come to trust an
orchestrator. Rationale: matches the "none of the worry" promise, demos the gate
viscerally, and keeps the safe default safe.

### FUTURE (revisit before v1.0): destructive auto-approve rules

Some advanced use cases will want to auto-approve even destructive actions (e.g. a
trusted CI-style orchestrator). We will NOT block that, but it must be **harder and
non-default**: an explicit, opt-in, narrowly-scoped, expiring, loudly-ledgered
escalation — never the default, never a blanket toggle. Design deferred; do not
build now.

## Policy layer (makes orchestration usable)

Pure per-action clicking doesn't scale (an orchestrator dispatching 50 tasks can't
wait for 50 prompts). Scoped auto-approve rules, set once:

- e.g. *"for link A→B, auto-approve action-type `dispatch-task`, this session, up to N."*
- Bounded by **action-type / link / count / TTL**; destructive tier excluded by default.
- Visible, expiring, revocable — never silent forever-grants.
- **Rule creation/removal is itself privileged → ledgered** (a blanket auto-approve
  rule is a bypass vector; treat adding one as a sensitive act).

## Surfaces — daemon-enforced; TUI + CLI + webhook are clients

The gate (queue + policy + enforcement + ledger) lives at the **daemon** — the only
authority that sees across islands. TUI, CLI, and webhook are three front-ends to
one approval queue (same one-API-many-surfaces pattern as the rest of Dejima).

### TUI (interactive — default human surface)

- **Alert:** a pending approval flashes the announcement bar (same bar as updates)
  with a count badge — urgent, so it pushes.
- **Approval prompt** shows the full payload (never approve blind): requesting
  island/agent → action type → target island/agent → payload → risk tier →
  `[a] approve  [d] deny  [r] approve + rule…  [v] view full payload / link grant`.
  `[r]` escalates to a scoped auto-approve rule.
- **Approvals overlay:** the pending queue + active auto-approve rules, in the same
  trust-surface family as the unified grants view (Port + MCP + links).
- **Approval fatigue mitigations** (critical under prompt-everything): batch-approve
  similar pending actions; group by link/action-type; make the **destructive tier
  visually stand out** so it can't be rubber-stamped among benign ones.

### CLI (headless / remote / scripting / CI)

- `dejima approvals ls` — pending queue.
- `dejima approvals watch` — stream pending (operator-on-call or an external approval bot).
- `dejima approve <id>` / `dejima deny <id> [--reason]`.
- `dejima policy add --link A->B --action dispatch-task --max 20 --ttl 1h` / `policy ls` / `policy rm`.
- No approver present + no matching rule → **default-deny on timeout** (fail safe).

## Non-negotiables

1. **Fail-safe:** unapproved-within-window = **denied**, not pending-forever.
   Destructive never auto-approves (until the future opt-in escalation).
2. **Everything ledgered:** every approve / deny / auto / timeout with
   who / what / when / decision / payload-hash — plus rule create/remove. The gate's
   whole value is that it's *provable*.
3. **Authenticated requests:** the action request carries the requesting island's
   authenticated identity (token-path), tamper-evident — the gate can't be spoofed/replayed.
4. **Clean denial signal:** the requesting agent gets a machine-readable
   `denied(reason)` — no crash-loop, no retry-storm.
5. **Non-blocking queue:** multiple approvals pending in parallel; no head-of-line
   block stalling an orchestrator's other actions.

## Team routing (ties to Lane 2 roles)

- **Owner/operator** can approve; **viewer** cannot.
- In a team, specify who receives the prompt (owner? any operator? the link's
  creator?) — solo collapses to "you." (Open — decide with Lane 2.)

## Directionality — what this gate covers (and what it doesn't)

The action gate governs **island → other island** actions (the orchestration case).
Other permission directions are governed by their own mechanisms; together they
should feel like ONE governance model (deny-all default, grant=envelope, ledgered,
surfaced in the unified grants view):

| Direction | Governed by |
|---|---|
| island → other island (actions) | **this action gate** |
| island → host files | Port (brokered, deny-all grants) |
| agent → MCP tools | MCP brokering (deny-all grants) |
| island → daemon control plane | **blocked** (island tokens can't create/delete/grant) |
| inbound external → island | **sealed** (no port publishing; future: mailbox-brokered ingress) |
| island → outside world (egress / LLM API) | **ungated today** — NOT covered. A future "egress gate" (approve outbound calls) is a possible governance ask; out of scope now. |

**Coherence goal:** unify Port + MCP + cross-island (+ any future egress gate) under
one grant + gate + ledger model and one "grants" view, so the operator faces a single
mental model instead of three bespoke approval systems.
