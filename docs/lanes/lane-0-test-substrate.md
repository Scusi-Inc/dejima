# Lane 0 — virgin-Mac test substrate  (HUMAN-OWNED)

> **Not an agent lane.** This is the clean-environment prerequisite that lets
> Lane A (install) and Lane B (uninstall re-adopt) be *proven* rather than asserted.
> Owner: human tester (the DejimaQA account is a non-admin *harness* account, not a
> virgin box — Docker/colima/brew/`~/.dejima` must never have run).

## Goal

A repeatable clean macOS environment where install + re-adopt can be demonstrated.

## Tasks

1. Provision a fresh macOS user **or** throwaway VM with no Docker / colima / brew /
   `~/.dejima` history. Document the exact reset procedure so every run starts virgin.
2. Decide host: VM on Minion vs. a fresh local user vs. ephemeral cloud Mac.
3. Script the teardown → install → assert loop so Lanes A & B can invoke it.

## Hand-off to the build lanes

Lane A and Lane B each ship an **in-island** test path (Go tests + a Docker
`scripts/integration.sh` extension) plus a **documented virgin-Mac proof procedure**
in their PR. This lane *runs* those procedures on the clean box and reports pass/fail.

**Done when:** Lanes A and B can each run their proof against a guaranteed-clean Mac,
and the reset procedure is written down.
