# Bounded delegation: rules for agents with real authority

It's 2am. A customer emails one of your companies about a disputed charge. That
company's agent wakes, reads the customer's history, and issues a refund through
a tool that can't exceed the day's budget. In the morning you see one card:
what it did, why, and the one thing it wants you to decide.

That's the job Dejima is being pointed at: letting an agent hold *real*
responsibility without holding *unlimited* authority. Containment alone doesn't
get you there. These five rules do. They came out of an adversarial review in
October 2026, and each one exists because the obvious design fails.

Each rule says what's built today and what isn't. Read the "missing" lines as
the backlog, not as aspirations.

## 1. The credential never enters the island

Money keys, banking tokens, anything whose misuse can't be undone: the island
never holds the raw value. The agent gets a narrow, typed tool, and the host
runs it with the real credential.

Why: a capped tool is worthless if the agent also holds the key. One `curl` to
the provider's API walks straight around any limit you wrote in the tool. The
cap itself is four lines of code. What makes it a boundary is that the agent
has no other path to the provider.

- **Built:** the capability broker (`internal/capability`,
  [capability-broker-spec.md](capability-broker-spec.md)) and the MCP broker
  (`internal/mcpbroker`, [mcp-broker-spec.md](mcp-broker-spec.md)). Both are
  deny-all, granted per island, run on the host, and write every call to the
  ledger.
- **Watch out:** island secrets (`dejima secret`) are injected into the island
  as environment variables. That's right for an npm token. It's wrong for a
  Stripe secret key. Nothing stops an operator from putting one there today.
- **Missing:** a way to mark a secret as host-only, so it can back a brokered
  tool but can never be materialized into an island.

## 2. Budgets per time window, not caps per call

A tool limit is a budget over a window ("$200 of refunds per day, 10 calls per
hour"), with operator approval required above a threshold. A cap per call is
not a limit.

Why: a $50 cap per call does nothing against fifty calls. A runaway loop or a
successful prompt injection doesn't need a big call. It needs many small ones.

- **Built:** `spawn` grants are a budget (`internal/spawn`). The action gate
  queues anything not pre-authorised for the operator
  ([action-gate-spec.md](action-gate-spec.md)).
- **Missing:** windowed budgets and rate limits on capability and MCP calls.
  Neither broker counts calls or sums amounts today.

## 3. The reader of untrusted input is not the actor

The agent that reads raw inbound text (email, PDFs, webhook payloads, anything
a stranger can write) holds no shell, no secrets and no authority. It turns
what it reads into structured requests. A different island acts on those
requests, never on the raw text.

Why: inbound mail is where prompt injection arrives. An agent with a shell that
reads attacker-written text has handed the attacker a shell. Walls between
islands limit how far that goes. They don't stop it happening inside the wall.

- **Built:** the home-island pattern
  ([runbook-openclaw-home-island.md](runbook-openclaw-home-island.md)), and
  links between islands that are denied until granted
  ([inter-island-exchange-spec.md](inter-island-exchange-spec.md)).
- **Missing:** enforcement. Today the split is a convention an operator
  follows. Nothing refuses a secret or a capability grant on the island that
  reads raw mail.
- **Residual risk:** an injection that gets through still reaches whatever the
  acting island can read. Keep each acting island's data to what its job needs.

## 4. Routine work takes the deterministic path

Webhooks, deadlines, routine refunds and syncs go through deterministic code or
a cheap model. A full agent wakes only for work that needs judgment: a dispute
that means reading history, a contract to review.

Why: cold-starting a frontier agent costs real tokens and real seconds. Its
prompt cache expired hours ago, and it rereads its context to make a decision a
function could have made for a tenth of a cent. Tokens are most of the bill.

- **Built:** scheduled wakes and wake-on-message
  ([scheduled-wake-spec.md](scheduled-wake-spec.md)). Token cost tracking from
  the agent's own usage hook (`internal/usage`).
- **Missing:** a triage step before the wake, and a circuit breaker that stops
  an island whose spend climbs without progress.

## 5. Resume is driven by state, not by processes

Waking an agent means: start the container, launch the agent with its *native*
resume flag, and hand it the task. Its memory lives on disk: the ops repo, the
island's volumes, the ledger. Nothing snapshots process memory, and nothing
scrapes a terminal buffer.

Why: process snapshots (`docker pause`, CRIU) break on every Docker, macOS or
agent CLI update, and `docker pause` doesn't free memory anyway. State on disk
survives all of those. It also keeps waking cheap. An on-call agent should come
back by reading a short summary, not by replaying two days of conversation.

- **Built:** `dejima upgrade` relaunches every agent with its resume flag
  (`handlers.Handler.ResumeLaunch`), and `containerResumesPrimary` keeps the
  primary agent and the others consistent
  (`internal/api/resume_consistency.go`).
- **Built (#333):** the caller decides at each start, not the container's
  history. The daemon writes the primary's launch to a read-only mounted file
  before every start it initiates, and `image/start.sh` prefers it over the
  launch baked in at create (`internal/api/launch_intent.go`). An operator's
  `dejima wake` resumes.
- **Cold on purpose:** unattended wakes (scheduled, wake-on-message) and
  unpanic start fresh and rely on what's on disk. `claude --continue` picks the
  most recent conversation in that directory, which may not be the agent's own,
  and an emergency stop shouldn't resume the agent that caused it.
- **Rollout:** an island picks this up once it's recreated on a rebuilt image
  (`dejima image`, then `dejima upgrade`). Until then it keeps its old
  behaviour, and the daemon follows it, so its agents never split.
