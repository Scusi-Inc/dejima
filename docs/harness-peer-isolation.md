# The harness has its own channel, and Dejima is not on it

**Status:** shipped (the outbound half). The inbound half is measured, documented
here, and deliberately not defaulted — it severs in-island messaging. See "What
is NOT defaulted" below.

## What was found

On 2026-09-13 an operator asked an agent in one island to "get everything
pushed, check with other agents as well." It reported back that it had messaged
two agents named `lineup` and `harbormaster` — sessions on other machines,
working on unrelated repositories. That should not have been possible.

Measured from inside an island container:

```
$ dejima link ls
Error: route not permitted for an island token
```

Dejima's own gate held perfectly. The island token cannot enumerate link grants,
let alone send across one. The containment model was never involved.

The same container then listed **57 peer Claude Code sessions** across the
operator's entire account — other machines, unrelated projects — and could
message any of them. That goes through Claude Code's Remote Control: an
outbound WSS connection from the island to Anthropic, authenticated by the
operator's own account. It does not touch the Dejima daemon, is not subject to
any Dejima grant, and does not appear in the Ledger.

No rule was broken. A second, ungoverned channel exists beside the governed one.

## Why it matters more than it first looks

The risk is not primarily exfiltration. A message to another agent session is
**instruction-shaped input to something holding a shell**. An agent in a
contained project container can hand work to an agent sitting in a different
repository on a different machine, and receive instructions the same way. The
blast radius of one confused or prompt-injected agent becomes the account rather
than the container.

## The fix that does not work, and why it is worth recording

The first instinct was: Remote Control is opt-in via `--remote-control`, Dejima
never passes it, therefore island agents are unaffected. A second agent in the
same island, launched without the flag, appeared to confirm this — it had no
peer visibility and coordinated normally over the in-island mailbox.

That conclusion was wrong, and the way it was wrong is the interesting part.

The island measured above was launched as plain `claude` — no flag, `ps` and
`DEJIMA_LAUNCH` both confirm it — and Remote Control was connected anyway. It
auto-enables server-side (`~/.claude.json` carries the `remote-control-auto-on`
notice; the harness ships a `tengu_ide_rc_auto_enable` feature flag). The second
agent was not contained by design. It had not been auto-enabled *yet*.

So the flag was never the mechanism, and **a convention about launch flags is
not a gate** — in this case it was not even a convention that held. This is the
`readings-go-stale.md` shape: a measurement that was true when taken, describing
a rollout state rather than a property.

Two further non-fixes, for the record:

- **Partitioning by API key or Console workspace.** Sound in general, and
  inapplicable here: the account is an OAuth Max subscription with a single
  personal organization. There are no API keys to segment. Executing it means
  one paid subscription per trust domain.
- **`chmod` the harness config read-only inside the container.** The island's
  agent has passwordless `sudo`. A permission bit is not a boundary against
  someone who can change the permission bit.

## What actually gates it

The harness reads policy from `managed-settings.json`, and managed settings
outrank both user and project settings. A file mounted **read-only from the
host** is therefore beyond the agent's reach — including an agent with root,
because the island's capability bounding set has no `CAP_SYS_ADMIN` and a
read-only bind cannot be remounted read-write. Verified in-island:

```
$ sudo touch /opt/host/claude/_probe
touch: cannot touch '/opt/host/claude/_probe': Read-only file system
$ sudo mount -o remount,rw /opt/host/claude
mount: /opt/host/claude: permission denied.
```

That is a gate: enforced by the kernel, outside the subject's sphere of
influence. Dejima already mounts secrets, gh config and gitconfig this way.

### What ships

`internal/api/harness_policy.go` materializes a per-island policy file on the
host and bind-mounts its **directory** read-only at `/etc/claude-code`. The
default:

```json
{
  "isolatePeerMachines": true
}
```

`isolatePeerMachines` requires explicit human approval before the agent can
message a session on another machine over Remote Control. Two properties earn it
the default slot: the harness marks it **bypass-immune**, so
`--dangerously-skip-permissions` cannot skip the prompt, and it is **not
classifier-routed**, so auto-mode cannot self-approve it.

What it does *not* do, all deliberate:

- Remote Control keeps working. Operator-to-agent steering from a phone or
  another machine is untouched — that traffic is a different ingress path
  (`host-injected`), not a peer message.
- In-island, same-machine agent-to-agent messaging is untouched.
- `dejima msg`, the ledgered channel, is untouched.

And three honest limits:

- **It gates sending, not seeing.** `ListAgents` still enumerates every peer
  session on the account. Project names leak across trust domains even with this
  on. No harness setting was found that hides peers.
- **It is an approval, not a deny.** An unattended island stalls on a
  cross-machine send rather than refusing it. The harness also prompts when it
  merely cannot *resolve* whether a name is local, so a transient fetch failure
  produces a prompt. Stalling on ambiguity is the right failure direction, but
  it is a stall.
- **It is create-time.** Bind mounts are decided once, inside
  `createContainerForProject`. An island that already exists is not subject to
  the policy until it is recreated. That population is identifiable rather than
  silent — the harness policy row appears in the credential mount report and
  drifts `configured=true, mounted=false`. `image/agents/claude-code/init.sh`
  also sets the user-settings equivalent as a fallback, where it is a default
  rather than a gate.

## Opting an island out

Edit the file on the host. It is materialized once and never rewritten, so an
operator edit survives container recreate:

```
~/.dejima/policy/harness/islands/<island>/managed-settings.json
```

Writable by the operator on the host, unwritable by the agent in the container.
That asymmetry is the entire design.

## What is NOT defaulted: `crossSessionInbound`

The harness has a second setting, and on the merits it matters *more* than the
one that shipped:

> `crossSessionInbound`: `'accept'` delivers inbound peer messages, `'hold'`
> parks them for review without letting Claude act, `'refuse'` opts this session
> out. Unset means mode parity — a message auto-delivers only when the sending
> session's permission-mode class matches yours.

Inbound is the direction that carries the risk, because inbound is the direction
that carries instructions. It is absent from the default because it was measured
and it **does** sever in-island coordination.

Reading the harness binary was inconclusive: the sender-side recipient-gate check
is scoped to `scheme === "bridge"` (the Remote Control transport), while the
receiver-side gate keys on `kind === "peer"` with no visible transport filter. So
it was tested instead, between two agents in one island on 2026-09-13 — `refuse`
set on the receiver, a one-word message sent from the peer. The sender's own
notice:

```
[Cross-session delivery notice] Your message to another session was refused
(recipient: uds:/tmp/cc-socks/57.sock): that session is not accepting
cross-session messages (the feature is off there, or a setting or policy there
refuses them). Not delivered to that session's Claude.
```

`uds:/tmp/cc-socks/57.sock` — a unix socket on the same box. The refusal is on
the LOCAL path, not the Remote Control one, so `refuse` is not Remote-Control-
scoped and would cut in-island agent-to-agent messaging along with the traffic it
is aimed at.

One detail from that test is worth more than the result, because it is how the
result nearly came out backwards. The sender's tool call returned
`{"success": true, ..., "msg_id": ...}` and the refusal arrived minutes later as
an out-of-band notice. **Both are true of the same send**: success meant accepted
for delivery, the refusal is what delivery returned. A prober who reads the tool
result and stops has a clean, good-faith success that says the opposite of what
happened — `readings-go-stale.md`, on a horizon of minutes.

To turn it on deliberately, add it to the same file:

```json
{
  "isolatePeerMachines": true,
  "crossSessionInbound": "refuse"
}
```

Prefer `"refuse"` over `"hold"` for islands: a held message on a headless island
expires and is dropped (`"headless: held peer message expired (no approval
surface)"`), whereas a refusal sends the sender a receipt.

**Enable it only on islands whose agents do not need to message each other**,
and expect `SendMessage` between agents in the same island to stop working there.
`dejima msg` is unaffected and remains the coordination path — which is arguably
the right end state, since it is the ledgered one, but it is a behaviour change
and not a free one.

## The claim that has to change

Dejima's documentation says crossings are "each deny-by-default and opened by an
explicit operator grant, with the crossings recorded." With this shipped, the
accurate statement splits in two:

- Dejima **gates** the harness backplane. Managed settings, read-only mounted,
  unwritable by the agent.
- Dejima **cannot ledger** it. There is no hook into Anthropic's relay. A
  cross-machine send happens with an operator approval and no Ledger entry.

Say both. A gated-but-unlogged channel described as "recorded" is the exact
shape this repo keeps failing on: a surface that reassures instead of failing.

## Residual, unfixed

`CLAUDE_CODE_MANAGED_SETTINGS_PATH` redirects the harness's policy walk. Dejima
controls the launch it performs; it does not control a `claude` subprocess an
agent spawns for itself with that variable set. An agent that wants past this
gate, and knows to want past it, can get past it.

That is worth stating plainly rather than papering over. The threat this
addresses is a confused or injected agent doing the obvious thing, which is the
one that actually happened. It is not a containment boundary against an agent
deliberately working to defeat it, and it should not be described as one.

## Reading

- `internal/api/harness_policy.go` — the mount and why each setting is there.
- `internal/api/harness_policy_test.go` — the inode trap, the operator-edit
  guarantee, the read-only assertion, the pre-existing-island drift signal.
- [testing/lifecycle-not-value.md](testing/lifecycle-not-value.md) — why
  create-time fixes do not reach containers that already exist.
- [testing/readings-go-stale.md](testing/readings-go-stale.md) — why the
  second agent's apparent containment was a rollout state, not a property.
