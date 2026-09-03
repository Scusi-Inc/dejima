# Folder import — spec

**Status:** specified, unbuilt. Owner: d3.

## The gap

Getting files INTO an island works, three ways, and every one of them is
single-file:

| Path | What it is | Folders |
| --- | --- | --- |
| `dejima cp ./f isl:/path` | daemon-brokered, `ReadFile`/`WriteFile` | no — single stream |
| `dejima port intake isl scope:rel` | brokered, scoped, **ledgered** | no — refuses with *"intake is single-file in V1"* |
| TUI drag-drop | uploads instead of typing a path | one file |

`port grant` already takes a **directory** as a scope. The permission model is
already folder-shaped; only the transfer is not.

Today the workaround is to tar it by hand, `cp` the archive, and untar over
`exec`. That works, and it is three commands, a temp file, and **no Ledger
entries at all** — the convenient path and the audited path are different paths,
which is the failure this repo keeps finding in other places.

## Shape: extend intake, do not add a command

`port intake --recursive`, not a new endpoint or a new verb.

Everything recursion needs already exists and is already per-file: the scope
check, the path-traversal refusal, read-normalization (a 0600 host file made
readable by uid 1000), and a Ledger entry per crossing. A walk that calls the
existing per-file path inherits those properties. A new transfer path would have
to re-earn every one of them, and would be the obvious place for one of them to
go missing.

Same for `dejima cp -r`: tar, stream, untar on the far side. Convenient,
UNAUDITED, and labelled as such in its own help text — the distinction between
the two is the product, not an implementation detail.

## Decisions, settled here so they are not settled by accident

**1. Do not follow symlinks. Ever.**
The V1 traversal refusal is per-file. A walk has to decide, and following is
precisely how a caller escapes a granted scope: a symlink inside the scope
pointing at `~/.ssh` reads as "a file in the scope" to a per-file check. Skip
them and report how many were skipped — silently omitting files is its own bug.

**2. One Ledger entry per FILE, plus a batch id.**
Per-operation reads better and is wrong: `--verify` walks a hash chain of
crossings, and a batch entry has no hash for the thing that crossed. Per-file
keeps the chain meaningful; the batch id makes the group readable. If only one
can ship, ship per-file.

**3. Caps, enforced before the first byte moves.**
A file count and a total byte size, both configurable, both refused UP FRONT
rather than discovered at file 400. "Import my home directory" must fail in a
second with a number in the message, not half-copy for ten minutes.

**4. Partial failure has a defined state, and the caller is told.**
If file 400 of 500 fails: the first 399 have crossed and are ledgered, and that
is fine — but the response must say what crossed, what did not, and why, and it
must not report success. Do not attempt a rollback: un-copying files is a
destructive operation invented to tidy up a failure, which is how the `agent rm`
bug happened. Report honestly instead.

**5. Refuse an empty result loudly.**
A recursive intake that matched zero files must say so, not report success over
an empty set. Same rule as the guards: "I copied nothing" and "there was nothing
to copy" are different sentences.

## Surfaces

- **API** — `recursive` on the existing intake operation, and the response grows
  a per-file result list. **Document it in `openapi.yaml` in the same commit**,
  fields included — route parity will not catch a new field, and Harbormaster is
  generated from that spec.
- **CLI** — `dejima port intake -r`, and `dejima cp -r`.
- **SDK** — falls out of the spec.
- **TUI** — a folder drop becomes the same call with `recursive`. Placed in the
  island settings group described below.

## TUI placement

Secrets and file import are not settings. They are **the two things you do TO an
island from outside it** — everything else in that menu is configuration. So
they get their own group at the top, under the up-a-level items, separated by a
double rule:

```
  ‹ Back
  ═══════════════════════
  ⇅  Import files…
  🔑  Secrets…
  ───────────────────────
  Restart agent…
  Erase all agent memory…
```

The separator earns its place: those two cross the island boundary and the rest
do not. That is the same distinction the grants pane draws, and it is worth being
consistent about — adoption (`agent-adoption.md`) makes "what crosses" the
central idea.

**Check the key against `tui_grants.go`'s enumeration before choosing one.** `R`
already means three things in one surface and cost an operator a session; that
enumeration makes a removed kind a compile error rather than a test failure, and
it is the right place to look before adding a fourth meaning.
