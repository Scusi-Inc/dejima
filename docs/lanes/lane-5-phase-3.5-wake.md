# Lane 5 — Phase 3.5: wake-on-message + recipient action-handler contract

Phases 1–3 (mailbox, info link, action gate) **deliver, authorize, and audit** — but
nothing makes an *idle* recipient look. A message/action sits in the mailbox until the
agent polls, and an idle agent isn't running to poll. Phase 3.5 is the piece that turns
"stored + authorized" into "the agent actually acts." Read first:
`docs/inter-island-exchange-spec.md`, `internal/mailbox`, `internal/link`, the
idle-hibernate code (0.5), the agent-liveness/heartbeat path behind the
`dejima_agent_idle_seconds` metric, and the agent-handler registry (`internal/handlers`).

## Why this lives in the substrate (not the app)

The *mechanism* — reaching into a contained agent's session to deliver a signal, or
waking a hibernated one — can only be done by Dejima; nothing above it can touch a
sandboxed session. It's the **twin of idle auto-hibernate**: hibernate-on-idle ↔
wake-on-message, exactly the actor-runtime model (Erlang/OTP; Rivet "Actors" wake the
idle/hibernated actor on an incoming message). The *policy* (when to wake, whether to
interrupt, routing/priority) is app-overridable, but Dejima ships a sane default so it
works with no wrapper.

## Part A — recipient action-handler contract (doc + light enforcement)

- A cross-island **action** arrives as a **daemon-stamped** `mailbox.Message` with
  `Action{Type,Params}` set and `Origin.CrossIsland=true`. Recipients act **only** on
  such messages — never on free-form `Payload`. The daemon-stamp is the trust signal
  (agents cannot forge `Action`/`Origin` — only `DeliverAction`/`DeliverExternal` set them).
- The recipient dispatches on `Action.Type` (a verb it exposed). Unknown/unexposed
  type → ignore (it shouldn't arrive; defense-in-depth).
- Document this as the per-runtime integration contract; provide a reference handler.

## Part B — wake-on-message (substrate primitive)

1. **Wake primitive paired with hibernate.** On mailbox arrival for agent X: if X's
   island is hibernated, wake it; then deliver a signal into X's session.
2. **Default soft-notify policy (info tier).** Batched **pointer**, not a payload dump
   ("📬 N new messages — `dejima msg poll`"), delivered at the **next turn boundary**
   (never interrupt mid-turn). De-dupe: one nudge per quiet period, not per message.
   - *Idle* agent (at its prompt) → inject as next input → wakes it.
   - *Busy* agent (mid-turn) → queue; deliver at the turn boundary.
   - Turn-boundary / idle detection reuses the agent-liveness heartbeat (the
     idle-seconds path), not a new mechanism.
3. **Hard interrupt = an action, not a notify flag.** "Stop what you're doing now"
   *does something to* the target (clobbers in-flight work) and is a DoS vector if
   ungated. Route it through the **Phase-3 action gate** (deny-all + pre-auth/approval +
   audited) as an exposed action type (e.g. `interrupt`) whose handler signals the
   agent to halt. Do **not** add an "interrupt" flag to ordinary messages.
4. **Per-adapter injection shim.** How to deliver into a session differs by runtime
   (Claude Code: inject at the prompt / hook; OpenClaw: polls its own loop; Letta/Goose:
   their input path). Add a small inject seam alongside the handler registry; default =
   PTY write at the turn boundary.
5. **App-layer hooks.** Expose mailbox-arrival on the existing events stream and a
   config to override the default policy, so a wrapper can implement custom
   routing/priority. Mechanism in Dejima; advanced policy overridable.

## Abuse / safety

- Soft-notify can't interrupt work (turn-boundary only) → no DoS via info spam.
- Hard interrupt is gated (action tier) → can't be abused; every interrupt audited.
- Waking an island has cost → batch + rate-limit; respect resource controls/hibernate.

## Owns / don't touch

Owns: `internal/mailbox` (arrival hook), the session/PTY inject seam, the wake↔hibernate
integration, the events `mailbox-arrival` signal, the per-adapter shim, config. **Don't
touch:** the link gate internals (shipped), audit/auth/MCP.

## Workflow + verification

Branch `feat/link-wake`; `go build`/`vet`/`golangci-lint run ./...`/`go test ./...`
green; unit-test the policy (turn-boundary vs idle, batching/de-dupe, wake-from-hibernate,
the gated-interrupt path). Per-adapter *live* behavior (does Claude Code actually wake?)
is **Minion-verified, batched with the rest of the inter-island wave**:
- Idle agent receives mail → woken (or sees it next turn); hibernated island wakes on message.
- Busy agent is **not** interrupted by soft-notify; no notify-spam (batched).
- A hard-interrupt action halts a busy agent **only** when gated/approved; ledgered.
