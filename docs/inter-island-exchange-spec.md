# Inter-agent & inter-island exchange — design & plan (Lane 5)

**Status:** design ratified 2026-06-21; not yet built. A conscious revisit of
[`positioning.md`](positioning.md) (which lists inter-island comms as out of scope).

## Why / context

Agents sometimes need to coordinate. Today they can't across islands (containment
invariant), and there's no messaging primitive even within an island. We add this
**without punching a hole in containment** — the same brokered, deny-all, scoped,
audited posture as Port and the capability/MCP brokers.

The design is validated by where the field landed:
- **A2A** (Agent2Agent — Google + LangChain/PayPal, the inter-agent standard) cleanly
  separates **state/context sharing** (Secure Passport) from **action delegation**,
  and mandates "enforce authorization before sensitive actions" — but leaves the
  *mechanism unspecified*. That gap is exactly what Dejima provides.
- **OWASP Top 10 for Agentic Apps (2026)** rates agent-to-agent a top new attack
  surface (a prompt-injected agent can "propagate across connected agents") and
  prescribes **tiered approval**: read-only auto · write one-click · destructive
  detailed review.
- Orchestration **frameworks** (LangGraph/CrewAI/AutoGen/OpenAI handoffs) are
  hub-and-spoke wrappers that drive each island via the public API — they don't need
  peer-to-peer island comms.

## Principles

1. **Two tiers, different default postures** — *info exchange* (read/share) vs
   *action delegation* (trigger). The dangerous one is action.
2. **Daemon-enforced gate (exchange-down)** — every decision runs in `dejimad`,
   outside any island's blast radius. A compromised/prompt-injected agent can only
   *emit a request*; it can never self-approve, reach another island directly, or
   widen its own grants.
3. **A2A-shaped, not a silo** — speak the standard for interop; Dejima is the
   **brokered, host-enforced, audited authorization layer A2A leaves open.**
4. **Narrow scope** — native comms is for the *no-wrapper* case (e.g. a Home Island
   coordinating its own sub-agents). Broad orchestration stays the wrapper's job.

## The model — three layers

1. **Intra-island mailbox** *(ship first; near-zero risk).* Agents in one island
   already share `/workspace` + home; add a lightweight typed mailbox/blackboard +
   notify. No containment change.
2. **Inter-island INFO exchange** *(low risk).* Brokered, operator-granted, scoped,
   **audited** — read / notify / state-share (A2A "passport"-shaped). Deny-all default.
3. **Inter-island ACTION delegation** *(high risk — gated hard).* One agent causing
   another to *act*. **Deny-all, and a channel grant is NOT sufficient.** Each action
   requires **per-action authorization**: an operator-pre-authorized action type on
   that channel, *or* a human-in-the-loop confirm. Fail-closed. Every action ledgered.

## The gate (how it's unbypassable by design)

| Property | Guarantee |
|---|---|
| Lives in `dejimad` (host) | The authz decision is outside every island's reach. |
| Default-deny + fail-closed | No grant, or any error, → denied. |
| Host-enforced, not agent-trusted | Agents can't talk past it; they only submit requests. |
| Per-action authz for the action tier | A channel grant moves info, not actions. |
| Tamper-evident audit | Every request + decision in the hash-chained ledger. |

This is the strongest available posture (matches `security-boundary.md`): the
attacker is on the wrong side of the boundary, and nothing is silent.

## Grant model

- **Operator-only grants** (an island can't open its own channel) — CLI mirrors
  `dejima port`/`cap`/`mcp`, e.g. `dejima link grant <A> <B> --topic <t>
  [--actions <list>]`; deny-all default; directional (A→B); named topics / typed
  payloads.
- **Daemon is the only broker** — no direct island↔island socket; all traffic flows
  through `dejimad`.
- **Ledgered** — `link.grant` / `link.message` / `link.action` / `link.deny`.

## A2A alignment

Wire format A2A-shaped (JSON-RPC/SSE; Agent Cards for capability discovery). The
daemon mediates discovery, enforces the deny-all + per-action gate, and ledgers
everything. Result: interop with the A2A ecosystem *and* we own the missing
enforcement layer. Positioning: **"the substrate that makes A2A/MCP safe to run on
hardware you own."**

## Orchestrator pattern (the cross-island-agency question)

There is **no special "orchestrator" role with implicit elevated agency** — that
would violate deny-all. An orchestrator is simply **an island (often a Home Island)
the operator has granted multiple action-channels to**; its reach is *composed from
grants*, each scoped and audited. Broad, free-form orchestration remains the
**wrapper's job** (app-level, over the public API). So orchestration is expressed in
the grant model, not as an exception to it. (The `owner`/`operator`/`viewer` roles
govern human/token access — a separate axis from agent agency, which is always
grant-based.)

## Explicitly OUT (the hard boundary)

Ambient mutual visibility · an open message bus · direct island↔island sockets ·
cross-island filesystem mounts · agents self-granting · any implicit orchestrator
privilege. Containment stays the default; exchange is always an explicit, logged grant.

## `positioning.md` change

Keep "containment-first" as the headline thesis; add a documented **brokered-exception**
clause ("islands are blind by default; cross-island exchange exists only as an
explicit, scoped, audited grant — info freely, actions only with per-action
approval"). Land this with Phase 2.

## Implementation plan (phased; each its own PR + Minion live-verify)

- **Phase 1 — intra-island mailbox.** ✅ shipped (#24). `internal/mailbox` store +
  `POST/GET /v1/islands/{name}/mailbox` + `dejima msg` CLI; ledgered.
- **Phase 2 — inter-island info channel.** ✅ shipped (#25). `internal/link` grant
  store + gate; a granted A→B message is addressed to a specific agent and delivered
  INTO that agent's mailbox (no separate inbox); structured daemon-stamped
  `Origin{source_island,cross_island}`; `link.*` ledger; `dejima link` CLI;
  positioning.md brokered-exception landed here.
- **Phase 3 — inter-island action delegation.** ✅ shipped (#27). Named/typed actions
  only (deny-all + `{B exposes} ∩ {grant.Actions}`); an async approval queue
  (in-memory → fail-closed on restart + TTL) with operator approve/deny (agents can
  never self-approve); the gate is re-checked at approval time;
  `link.action`/`link.deny`/`link.approve` ledgered. Execute = the daemon delivers a
  daemon-stamped typed `Action` into the target agent's mailbox; the recipient runs
  its handler (see Phase 3.5).
- **Phase 3.5 — wake-on-message + recipient action-handler contract.** The piece that
  makes the above actually *fire*: a delivered message/action sits in the mailbox
  until the recipient looks, and an idle agent isn't running to look. Wake belongs in
  the substrate — the actor model (Erlang/OTP, Rivet "Actors") wakes the
  idle/hibernated actor on message; it's the twin of Dejima's idle auto-hibernate.
  Scope: (a) a daemon **wake-on-message** primitive paired with hibernate; (b) a
  default **soft-notify** policy (batched pointer, delivered at the next turn boundary,
  never mid-turn) for info; (c) **hard interrupt routed through the Phase-3 action
  gate** — interrupting another agent is *doing something to it* (gated + audited),
  not a free message flag; (d) a small **per-adapter injection shim** (Claude Code /
  OpenClaw / Letta / Goose differ); (e) **app-layer hooks** (the mailbox-arrival event
  + a policy override) so wrappers can do richer routing — mechanism in Dejima,
  advanced policy overridable. Plus the **recipient contract**: agents act ONLY on
  daemon-stamped `Action` messages, never on free-form payload. Brief:
  `docs/lanes/lane-5-phase-3.5-wake.md`.
- **Phase 4 (optional) — full A2A discovery/interop.** Agent Cards + external A2A
  peers, still behind the same gate.

## Verification (cross-island scenarios, on Minion)

- Deny-all by default (no grant → refused, ledgered `link.deny`).
- Granted info flows A→B and is ledgered; B cannot reply on an ungranted channel.
- An action with no per-action authz is **refused** even on a granted info channel;
  an authorized action runs and is ledgered.
- A compromised/injected island **cannot self-approve** an action or reach a
  non-granted island.
- Orchestrator pattern: a Home Island with N granted action-channels coordinates
  them; revoking a grant immediately severs that reach.
- A2A interop smoke (Phase 4).
