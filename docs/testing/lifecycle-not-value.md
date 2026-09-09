# A claim about what already exists is a lifecycle question wearing a value question's clothes

[`guards-need-controls.md`](guards-need-controls.md) asks whether a check has a
**subject**. [`readings-go-stale.md`](readings-go-stale.md) asks whether a reading
is **current**. This file asks a third question, about the code you are reading
rather than the check or the reading: **when does it run?**

The failures here all have the same shape. Someone asks "will existing islands
pick up X now?", and the answer is looked up by reading the *function* that
produces X. The function is read correctly, from source, often while explicitly
refusing to trust a merge message or a changelog. And the conclusion is still
wrong, because what changed was the value the function returns and what mattered
was the moment it gets called.

## The rule

**Before accepting "X now works for existing Y", find the call site and its
lifecycle — not just the callee.**

Say it out loud: *when does this run?* If the answer is "at create time" and the
claim is about things that already exist, the claim is false no matter what the
function returns.

## The case this came from

On 2026-09-03, three agents in a row concluded that a fix to
`islandLLMConfigDir` meant existing islands would pick up a provider key.

The function does now return the directory unconditionally. That part was
verified properly, from the source. But `credentialBindMounts` is called inside
`createContainerForProject` and nowhere else, and nothing can add a bind mount to
a container that already exists. Each agent opened the right file and asked the
wrong question.

It shipped to master described as "No island recreate" — the exact opposite of
the truth — before being caught.

The bug being fixed had the same shape as the mistake made while fixing it: a
claim true of INTENT reported as true of what is RUNNING. The store has the key,
`provider ls` lists it, and the container has no mount.

## What it looks like from the inside

| the question asked | what gets read | what decides the answer |
| --- | --- | --- |
| "will existing islands get the key?" | the config-dir function | where `credentialBindMounts` is called |
| "does the agent run the new version?" | the package on disk | whether the process was relaunched |
| "is the secret in the island?" | the secret store | whether the mount predates the write |
| "does the guard cover this?" | the guard's logic | which directory it was handed |

In every row the left column is answerable in one file and the right column is
not, which is exactly why the left column gets answered.

## How to apply

- Grep for the constructor or create path before concluding anything about
  existing state. `createContainerForProject` is where this repo hides the
  answer more often than not.
- Distinguish the three states out loud, because English collapses them:
  **on disk**, **in the running process**, **applied to existing instances**.
  "Updated" means the first; surfaces report the third.
- When you write the claim down, name which one you mean. "Installed, not yet
  running — restart required" is a sentence that survives review. "Fixed" is not.
- The answer is frequently a comment one file away, written in the imperative by
  someone who hit this before. `credential_mounts.go` opens with one.

## Why this is a doc and not a comment

It is here under protest, and the protest is the point:
[`docs/README.md`](../README.md) states the order — **enforce > co-locate >
contextual pointer > central doc** — and this file is the weakest of the four. It
earns its place only as a name for a shape that recurs, so the shape can be
pointed at in review.

The stronger move, whenever the specific instance allows it, is a check. If you
find yourself writing a fourth paragraph explaining that a create-time code path
cannot retrofit an existing container, write the gate instead.
