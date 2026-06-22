# Lane 5 — Inter-agent & inter-island exchange

You are the **Inter-island** agent. Build brokered agent↔agent / island↔island
exchange **without breaking containment**. The full design + phasing + verification
is the spec — read it and follow it exactly:

**→ `docs/inter-island-exchange-spec.md` (your authoritative plan).**

Also read: `docs/positioning.md` (the thesis you're consciously amending),
`docs/security-boundary.md` (exchange-down — the gate must live in the daemon),
`internal/capability/` + `internal/port/` (the broker pattern you mirror),
`internal/ledger/` (ledger entries).

**Non-negotiables (from the spec):**
- **Two tiers:** info exchange (brokered + granted + audited → allowed) vs **action
  delegation (deny-all + per-action approval + audited)**. A channel grant moves
  info, never actions.
- **Daemon-enforced gate** — outside every island's blast radius; default-deny,
  fail-closed; agents can only *request*, never self-approve or reach an ungranted
  island. Every grant/message/action ledgered (`link.*`).
- **A2A-shaped** wire format (interop); Dejima is the authorization layer A2A leaves
  open. Don't invent a bespoke protocol where A2A fits.
- **No special orchestrator role** — agency is composed from operator grants only.

**Phase it (each its own PR + Minion live-verify):** (1) intra-island mailbox →
(2) inter-island info channel **+ the `positioning.md` brokered-exception edit** →
(3) action delegation gate → (4 optional) full A2A discovery.

**Workflow:** per-phase branches (`feat/mailbox`, `feat/link-info`,
`feat/link-action-gate`); `go build`/`vet`/`golangci-lint run ./...`/`go test ./...`
green; small PR each to `master`; stay in your own island/worktree.
