# Adopting agents Dejima didn't launch — design

**Status:** design, unbuilt. Phase 1 (adopt-and-observe) is approved to build;
Phase 2 (graduation) is specified here so Phase 1 does not foreclose it, and
deliberately not started.

## The claim this changes

Today Dejima says *run your agents in containers*. That is a **migration**: it
asks for a decision and a move before it gives anything back.

Adoption makes it *whatever you are already running, Dejima can see it, ledger
it, and gradually pull it into containment*. That is an **adoption ramp**.
Someone with six agents running loose in terminals gets value on day one without
moving anything, and containment becomes something they opt into per agent, when
it suits them.

That matters here more than the general argument suggests. Install is our worst
friction — the fresh-Mac path took four defects and weeks of field failures and
is still only half-verified ([`test-coverage-matrix.md`](testing/test-coverage-matrix.md)
§19, and the open half in the verification queue). Adoption routes around
install-and-migrate entirely for first value.

## The risk, stated plainly

**Containment stops being binary and becomes a spectrum, while every claim we
make about it is currently binary.**

An adopted agent is not in an island. It can read the whole filesystem, reach
the network, and use any credential its user holds. If adopted and contained
agents appear in one list with the same affordances, "Dejima contains your
agents" quietly becomes a claim about *some* agents — which is worse than not
making the claim, because the operator cannot tell which.

This is not a hypothetical risk for this codebase. It is the defect this
codebase demonstrably produces. In two days we fixed:

- the grants pane reporting an island as contained after a revoke that had not
  taken effect;
- `dejima secret rm` reporting success while the value stayed readable inside
  the island;
- the host GitHub credential mounted into islands that never asked for it;
- and **"every agent is walled off from your machine, your files, and the other
  agents"** live on six surfaces including the homepage — when agents in one
  island share workspace, credentials and tool-auth.

The last one is the precedent to keep in mind. The *correct* fact was in
`api.html` the entire time. It simply never propagated into the copy people
actually read, and nobody noticed for weeks. Adopted agents are that same
failure with a much larger blast radius.

**Therefore: the structural distinction is built first, not retrofitted.**

## Design constraints

These are load-bearing. A Phase 1 that skips them is not a smaller Phase 1, it
is a different and worse feature.

### 1. An adopted agent is a different KIND of thing, structurally

Not a warning banner, not a badge in the same list. A separate section, its own
visual treatment, and its own affordances. An icon alone is a banner with fewer
words — it relies on the reader noticing and remembering, which is exactly what
failed on the homepage.

### 2. The grants view says what is true, not four empty arrays

An adopted agent has no grants because nothing gates it. Rendering that as empty
Port scopes, empty capabilities, empty MCP servers and empty links reads as
**sealed** when it means the **opposite**. It must say, in words:

> Dejima observes this agent but does not gate it.

Same rule as the GitHub credential notice, one level up.

### 3. Containment level is a FIELD IN THE DATA

Not a property of which implementation answered. If any consumer can learn
whether an agent is contained by asking *which source produced it*, the
abstraction has hidden the one guarantee that must never be hidden.

A `FleetSource`-style interface (dependency inversion at the data layer, so
"where the fleet comes from" does not leak upward) is the right shape for
transport. It is the wrong shape for guarantees. An interface that makes two
sources look identical at the type level is precisely the mechanism that makes
them look identical in the UI.

So: a non-optional field on the island/agent shape, such that an implementation
which fails to set it does not compile, and every consumer must handle it.

### 4. The ledger has THREE levels, not two

| | How Dejima knows | The honest sentence |
| --- | --- | --- |
| Contained | the daemon brokered the action | "this was allowed" |
| Contained, ungated path | the daemon observed it happen | "this happened" |
| **Adopted** | **it tailed a transcript the agent itself wrote** | **"the agent reported this"** |

The third is **self-reported**. An adopted agent — or anything that has
compromised it — can lie to the ledger, and omission is trivial: do not write
the line.

This matters because the ledger is what gets shown to a team lead. "Here is an
audit trail" is a strong claim. "Here is an audit trail, some rows of which are
the subject's own account of itself" is a different product. If adopted entries
are not visually distinct from brokered ones, the integrity claim of the whole
ledger degrades to that of its weakest row.

### 5. No partial-gating theatre

There will be pressure to offer half-measures — "Dejima won't hand it secrets."
An adopted agent already holds the user's entire environment; withholding
`dejima secret` from it is not containment, and a badge implying otherwise is
the failure mode in constraint 2 wearing a friendlier face.

Say **observed, not gated**. One honest sentence beats a spectrum nobody can
reason about.

## The technical asymmetry

**Observing is easy. Gating is hard.**

Dejima can tail an adopted agent's transcript, record its state, and put it in
the tray without any new isolation machinery. It cannot gate that agent's file
access without a sandbox — and a sandbox is most of what an island is.

This asymmetry is why the phases are ordered as they are, and why Phase 1 is
worth shipping alone.

## Phase 1 — adopt and observe (approved)

Scope: Dejima discovers, lists and tails agents it did not launch. It gates
nothing.

- **Containment level in the data model, first.** Before any adapter. The field
  and its honest rendering are the thing that makes everything after it safe.
- **Discovery** of local agents (transcript directories, running processes) —
  explicit and operator-initiated, never a background scan that surprises
  someone. **DEFERRED POST-1.0 (operator, 2026-09-01.)** Nothing else in Phase 1
  depends on it existing; it is what would make the rest *do* something, and it
  is the only piece needing host access outside Port. Full reasoning, and what is
  already banked, in the roadmap entry. Phase 1 therefore ships as the honest
  data model and the naming, with no producer of observed agents yet.

  **Decided before the deferral, and binding when it resumes — the scan's
  boundary.** Discovery runs on the DAEMON HOST, scans only the DAEMON USER's own
  processes and home, and NEVER crosses users — not even for an owner. Its output
  names which host was scanned, every time, unasked.

  The cross-tenant case decides it. The daemon has roles and owners, so a walk of
  process lists and home directories can enumerate another tenant's agents,
  including transcript paths that name their projects. That leaks the names of
  someone's work to a person holding no grant over it. Dejima's claim is that
  host access is scoped and audited; a feature reading other users' home
  directories because the daemon happens to run privileged would be the largest
  exception in the product, introduced for a convenience nobody asked for. If
  serving that case is ever wanted, it is a Port grant — brokered and ledgered —
  not a filesystem walk.

  A second, smaller consequence to design around rather than paper over: the
  command is typed on a CLIENT, which may be a different machine from the daemon.
  So an operator on a laptop driving a Mac mini discovers the MINI's agents, not
  their own. That is correct and it will surprise everyone, because "discover my
  agents" reads as local. Naming the host in the output is the honest minimum,
  not the fix; the command itself should be hard to misread.
- **Read-only state**: which agent, what it is working on, last activity,
  whether it is alive.
- **Ledger entries marked self-reported**, distinct at a glance from brokered
  ones.
- **Surfaces**: a separate section in the TUI list, the same treatment wherever
  islands are enumerated, and the grants view from constraint 2.

Explicitly NOT in Phase 1: any attempt to restrict an adopted agent, and any
implication in copy that adoption confers protection.

## Phase 2 — graduation (specified, not started)

Scope: turn an adopted agent into a contained one.

**"Pull it in" cannot mean moving a running process.** Retrofitting namespaces
and cgroups onto a live process is CRIU territory and not a foundation for a
product promise. Graduation means: stop the loose process, create the island,
bring its workspace and transcript across, and relaunch it contained and resumed
on the same conversation.

The hard half already exists — `claude --continue` / `--resume <id>` against
`~/.claude/projects/<slug>/`, and `a0bd706` already relaunches every agent on its
prior conversation through a real container recreate. Graduation is that plus a
workspace copy.

The verb is **graduate**, not *move*. Copy should say so; "pull in" implies a
continuity that is not being delivered.

Three hazards, each of which we have already met in other clothes:

1. **Graduation is when the operator discovers what their agent was actually
   using.** A loose agent has every credential and the whole filesystem; the
   contained one gets only what is granted. Things will break, at exactly the
   moment someone is trying the product's best feature. Requires a **preflight
   diff**: what this agent reaches now vs what the island will grant, shown
   before anything changes. Same lesson as the secrets banner — the surface must
   say what will change before it changes.

2. **Uncommitted work.** A loose agent's directory will routinely hold
   uncommitted and untracked changes. A graduation that clones the repo fresh,
   or copies only tracked files, destroys them — the identical failure to
   `dejima agent rm` running `git worktree remove --force` behind help text
   that said "keeps its branch". Reuse that guard; do not reinvent it.

3. **Host files cross through Port.** Copying a host directory into an island is
   what Port is for: brokered and ledgered. Graduation does not get a side door.
   A pleasant consequence — the first ledger entries for a graduated agent are
   real brokered ones, so the moment it becomes contained is the moment its
   ledger stops being self-reported.

## Why this order

Phase 2 depends on the containment-level field, the preflight diff, and the
dirty-worktree guard all existing. Building graduation first would ship the most
dangerous operation in the feature before the machinery that makes it honest.
