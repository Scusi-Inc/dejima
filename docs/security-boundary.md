# The security boundary: privilege exchange-down (ironclad rule)

This is the **non-negotiable** authorization invariant for every layer that sits
above the Dejima runtime — the Scusi control plane (`code-dispatch.com`), the
CLI/TUI, a Slack bot, any future UI. It governs how a trusted human's authority
relates to the authority an agent runs with inside an island.

It exists because of one specific, classic failure mode: the **confused
deputy / privilege inheritance** attack. If a high-privilege caller passes its
*own* credential down to a lower-trust executor, the executor inherits the
caller's authority and every containment guarantee collapses. Dejima's whole
reason to exist is containing **agents** (untrusted, possibly prompt-injected,
AI-generated code). That guarantee is only as strong as this boundary.

## The rule

> **A higher layer's identity is never inherited by an island. It is *exchanged
> down* at the boundary for an attenuated, per-island token. The master
> identity stops at the control plane; the island only ever holds the valet
> key.**

Master identity (Scusi / the human) ⟶ **[exchange-down boundary]** ⟶ attenuated
per-island token (the agent).

Authority only ever *decreases* as it crosses into an island. It must never
increase, and it must never pass through unchanged.

## What each side holds

| Layer | Identity it holds | Authority |
|---|---|---|
| **Human / Scisi control plane** | the master identity (full account, broad rights, the human's GitHub auth) | authenticates *humans*, drives lifecycle, grants Port scopes — the operator surface |
| **Island / agent** | a per-island bearer token (`DEJIMA_TOKEN`) + a per-island git identity | default-deny; only its *own* island's autonomy surface |

The human logging into Scusi with full rights and *then* dispatching an agent
does **not** hand the agent any of those rights. The agent inherits **nothing**
of the master identity — only the valet key minted for its island.

## The four concrete rules

1. **The master credential stops at the control plane.** Scusi's session
   credential (and the human's GitHub token, cloud creds, etc.) are *never*
   injected into a container, never readable from inside an island, never
   forwarded on a call the island can observe.

2. **An island only ever receives an attenuated, per-island token.** That token
   is default-deny and pinned to one island — it cannot reach the control plane,
   lifecycle ops, the attach surface, or Port-scope grants. Enforced in
   [`internal/api/tokenauth.go`](../internal/api/tokenauth.go); see
   [`secure-island-routing.md`](secure-island-routing.md) for why it is the
   *only* path in.

3. **Spawn attenuates; it never propagates a god-token.** When a Home Island
   creates a Project Island, the daemon mints a **child** token for the new
   island and returns it to the parent (`createIsland` →
   `CreateIslandResponse.Token`, only for a Home-token caller). The parent never
   shares its own token; the child is born with strictly its own island scope.

4. **Repo / tool authority is per-island, not the human's.** An agent pushes
   with its island's **own** git identity
   ([`github-identities.md`](github-identities.md)), not the human's GitHub
   token. Releasing an agent on a repo grants it *that island's* scoped
   credential — never the operator's.

## Forbidden patterns (these break containment)

- ❌ Injecting Scusi's session credential or the human's GitHub/cloud token into
  a container or its env.
- ❌ Letting an island token reach any control-plane, lifecycle, attach, or
  **Port-scope-grant** route (self-service host access for a contained brain is
  the cardinal sin — Port grants are operator-only by design).
- ❌ Letting an agent call *back up* to the control plane carrying the human's
  authority (the deputy executing with the caller's rights).
- ❌ A single shared "god token" handed to multiple islands, or a parent's token
  reused by its children.
- ❌ "Just SSH the human to the host and let them `docker exec` around" as the
  *product* access path — that is full host + all-island + secrets authority,
  the opposite of exchange-down. (Fine as an owner-operator escape hatch on a
  box you own; never the model Scusi builds on.)

## Where it is enforced today (already shipped)

- **Default-deny, island-pinned token middleware** — `internal/api/tokenauth.go`
  (`tokenRouteAccess`, `authorizeToken`): an island token reaches only
  `accessOwnIsland` autonomy routes; everything else is denied. Authorization is
  classified on the matched router *pattern*, so it can never diverge from
  routing (this closed a real `%2F` cross-island bypass — see
  [`port-island-work`] history).
- **Constant-time token resolution + 0600 storage** — `internal/porttoken`.
- **Single authenticated path in** — the operator control socket is *not*
  mounted into containers; islands reach the daemon only over the
  token-authenticated, host-internal TCP listener
  ([`secure-island-routing.md`](secure-island-routing.md)).
- **Child-token spawn** — `internal/api/server.go` `createIsland`: a Home token
  gets the child's token back; no god-token.
- **Per-island git identity** — [`github-identities.md`](github-identities.md).

The primitives exist. The job of every higher layer is to **use the
exchange-down path, never bypass it.**

## Conformance checklist for Scusi (and any control plane)

Before any layer above Dejima ships an "act on an island" feature:

- [ ] Does the island ever see a credential broader than its own per-island
      token? (must be **no**)
- [ ] Is the master identity used *only* to call the daemon's operator surface
      from the control plane — never threaded into the island?
- [ ] When dispatching an agent on a repo, does it get the island's own git
      identity rather than the human's token?
- [ ] Can an island, using only its token, reach any route this document
      forbids? (verify against `tokenauth.go`'s default-deny)
- [ ] Are Port-scope grants performed *only* by the operator/control plane, with
      the human's authority, and never reachable by an island token?

If every box is checked, the boundary holds and the agent runs with strictly
less authority than the human who launched it — no matter how the human
authenticated.

## Related

- [`secure-island-routing.md`](secure-island-routing.md) — the single
  authenticated in-island → daemon path (the enforcement layer).
- [`port-island-spec.md`](port-island-spec.md) — Port brokering (operator-only
  scope grants).
- [`github-identities.md`](github-identities.md) — per-island git identity.
- [`capability-brokering.md`](capability-brokering.md) — files-only brokering;
  never a general host-command broker (same containment philosophy).
- [`positioning.md`](positioning.md) — containment-first runtime thesis.
