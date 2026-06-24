# Operator test — `uninstall --keep-islands` re-adoption (Lane B)

**Run on a real Docker host (Minion / a clean Mac). This is the acceptance bar for
Lane B (P0.2 uninstall-safety).** ~10 min.

It proves the central guarantee of `dejima uninstall --keep-islands`: removing Dejima
does **not** destroy your islands. The named Docker volumes and `~/.dejima` config
survive, and a **fresh install re-adopts the pre-existing island by name** with its
workspace intact — so uninstall is not a one-way data-loss trap.

The in-island Docker portion of this is automated in `scripts/integration.sh`
(feature: *"uninstall --keep-islands + re-adopt"*). This operator test is the
**virgin-Mac** version: a real `curl … | sh` install, a real uninstall, a real
reinstall — the round-trip the automated suite can only approximate inside one $HOME.

Conventions: island **R** = `readopt-test`. Run as the operator on the host.

## 0. Setup — install + create an island with a workspace marker
```bash
# Fresh install via your normal channel (curl|sh, brew, or `make install`).
dejima ls                                   # daemon up, no islands (or your usual set)
dejima init --name readopt-test --repo <any-repo>   # create island R, let it come up
dejima ls                                   # EXPECT: readopt-test running

# Write a marker into the island's workspace volume.
dejima exec readopt-test -- sh -c 'echo readopt-survives > /workspace/keep.txt'
dejima exec readopt-test -- cat /workspace/keep.txt   # EXPECT: readopt-survives

# Note the deterministic volume + config names — these are what must survive.
docker volume inspect dejima-readopt-test-workspace   # EXPECT: exists
ls ~/.dejima/projects/readopt-test/config.toml        # EXPECT: exists
```

## 1. Bare `uninstall` refuses — no destructive default
```bash
dejima uninstall
#   EXPECT: refusal — "refusing to uninstall without an explicit choice", and it
#   names BOTH --keep-islands and --purge-all. Nothing is removed.
docker volume inspect dejima-readopt-test-workspace   # EXPECT: still exists
dejima ls                                             # EXPECT: readopt-test still there
```

## 2. `--keep-islands` removes Dejima but KEEPS the island
```bash
dejima uninstall --keep-islands
#   Confirm the prompt (type 'uninstall'), or pass --yes.
#   EXPECT, in the summary: "stop … (KEEPING their volumes + config)" and
#          "keep ~/.dejima", NOT "delete ~/.dejima" and NOT "purge".

# After it finishes the daemon + binaries are gone, but the island's state is not:
docker volume inspect dejima-readopt-test-workspace   # EXPECT: STILL exists
ls ~/.dejima/projects/readopt-test/config.toml        # EXPECT: STILL exists
# Prove the data itself is intact, with no Dejima involved at all:
docker run --rm -v dejima-readopt-test-workspace:/ws:ro dejima/island:latest \
  cat /ws/keep.txt                                    # EXPECT: readopt-survives
```

## 3. Fresh install re-adopts the island
```bash
# Reinstall via the same channel as step 0 (curl|sh / brew / make install).
dejima ls
#   EXPECT: the new daemon already lists readopt-test from the kept config
#   (it may show as hibernated/stopped — its container was removed, the volume wasn't).

dejima wake readopt-test                  # recreate the container against the kept volume
dejima exec readopt-test -- cat /workspace/keep.txt
#   EXPECT: readopt-survives  ← the workspace written BEFORE the uninstall is back.
```
If `dejima ls` does **not** list the island after reinstall (e.g. a future installer
that ships a clean config), re-create it with the same name — it binds the same
named volume by its deterministic name, so step 3's `keep.txt` check still passes:
```bash
dejima init --name readopt-test --repo <same-repo>    # re-adopts dejima-readopt-test-* volumes
dejima exec readopt-test -- cat /workspace/keep.txt   # EXPECT: readopt-survives
```

## 4. `--purge-all` is the only path that nukes
```bash
# (Optional, destructive.) Confirm the opt-in nuke really does delete everything.
dejima uninstall --purge-all --yes
docker volume inspect dejima-readopt-test-workspace   # EXPECT: no such volume
ls ~/.dejima 2>&1                                     # EXPECT: gone
```

## Pass criteria
- [ ] Bare `uninstall` refuses and names both choices (step 1).
- [ ] `--keep-islands` leaves the named volume **and** `~/.dejima` config on disk (step 2).
- [ ] The workspace marker written before uninstall is readable from the volume with
      no daemon running (step 2).
- [ ] After a fresh install + `wake` (or re-create), the island returns with
      `keep.txt` == `readopt-survives` (step 3) — **re-adoption proven.**
- [ ] `--purge-all` deletes the volume and `~/.dejima` (step 4).

## Notes for Lane 0
- `--keep-data` is a deprecated alias that now maps to `--keep-islands` (it used to keep
  config but still destroy volumes — a flag that lied; that bug is fixed). Spot-check that
  `dejima uninstall --keep-data --yes` behaves identically to `--keep-islands`.
- The volume/config names are deterministic: `dejima-<island>-workspace`,
  `dejima-<island>-home`, and `~/.dejima/projects/<island>/`. Re-adoption is "same name →
  same volume", which is why no extraction/export step is needed (the exit-ramp that turns
  volumes into plain host dirs is a separate P1, intentionally out of Lane B's scope).
