# Where a workspace comes from — spec

**Status:** specified, unbuilt. **Queued behind #362** (folder import), because it
is a thin wrapper over that work and must not become a second way to move host
files into an island.

## The question was always "what goes in /workspace?"

`dejima init` asks for a git repo. That was never the real question — it was one
answer to it, promoted to being the whole prompt. There are four honest answers
and three of them now exist:

| Source | Status |
| --- | --- |
| Clone a GitHub repo | shipped |
| Use a local repo | shipped |
| **Use a local folder — not a repo, just files** | **this spec** |
| Start empty | shipped (v0.8.67) |

The missing one is the most common thing a person actually has: **a directory of
work that is not a repo yet.** Scratch analysis, a folder of documents, a project
started before anyone ran `git init`.

## Why at create time, not as a second step

The two-step version — create empty, then import — will exist the moment #362
lands, and it works. It is still wrong for the common case.

At create time the operator is *already* answering "what should be in here?".
Making them create an emptiness and then fill it asks the same question twice and
leaves a wrong-looking island in between. One question, four answers:

```
Where should this island's workspace come from?
  Clone a GitHub repo
  Use a local repo
  Use a local folder          ← not a repo, just files
  Start empty
```

## It reuses Port. It does not invent a path.

Copying a host folder into an island is exactly what Port exists for: scoped,
brokered, ledgered. A create-time copy that bypassed it would be a **second,
unaudited way to move host files into an island** — which is the precise
divergence `docs/folder-import.md` exists to close, reintroduced at a different
door.

So this is a THIN WRAPPER over #362's recursive intake, not a new mechanism. It
inherits the traversal refusal, read-normalization, the symlink handling, the
caps and the per-file Ledger entries rather than re-earning them. If it starts
growing its own copy loop, it has gone wrong.

Implementation shape: grant a scope for the chosen directory, run the recursive
intake into the new island's `/workspace`, then drop the scope unless the
operator asked to keep it. The grant is the audit trail for how those files got
there.

## The thing that must not be fudged

**A copied folder is not a git repo, and the island must not pretend otherwise.**

The temptation will be to `git init` it so the agent's tooling behaves. Don't do
that silently. Three things break, each quietly:

- The agent commits into a repo the operator never made and cannot push
  anywhere. Work looks saved and is not.
- `purge`'s unpushed-work guard starts having opinions about a repo with no
  remote — either a false alarm every time, or a guard trained to be ignored.
- `agent rm`'s worktree logic assumes a real repo. The guard added for it
  (uncommitted work refuses removal) reasons about `git status` in a worktree;
  a fabricated repo makes that answer meaningless.

Offer `git init` as an **explicit choice with the consequence named**, or do not
do it at all. Defaulting it on is creating a state whose surface implies
something untrue — the failure this codebase has spent a week removing from four
other surfaces.

`NoRepo` already exists on `IslandInfo` for exactly this distinction. A
folder-sourced island is repo-less; it just is not empty.

## Surfaces

- **CLI** — `dejima init <name> --from ./folder`
- **TUI** — a fourth row in the create wizard's source list. Note the placement
  constraint recorded in #355: the cursor's zero value decides the default
  action, so a new row must not silently become what Enter-Enter does.
- **API** — a request field, documented in `openapi.yaml` in the same commit.
  Field parity will catch it if not, which is the point.

## Open questions

- **Size feedback.** A folder copy can be large and slow. The caps from #362
  apply; what is missing is progress, because a create that appears to hang is
  the failure mode that produced the Ctrl-C in the Docker install.
- **Ignore rules.** `node_modules`, `.venv`, build output. Copying them is
  usually wrong and always slow. A default ignore list is tempting and is a
  policy decision — silently skipping files an operator asked for is its own
  false surface. Prefer reporting what was skipped, loudly, over guessing well.
