# Operator hand-off — clean-Mac install/uninstall launch gate (Lane 0)

**Run on a real clean Mac (the Minion `dejimaqa` account). This is the P0 launch gate:**
it proves Dejima **installs, uninstalls, and re-adopts** on a virgin Mac for every channel —
the single biggest remaining launch risk. ~20–40 min depending on channels.

It is fully scripted and CI-runnable, but the **live run is yours**: it needs a genuinely
clean macOS host, which can't exist inside the dev island. The harness author delivers the
button and the assertions; the green tick is the operator's to earn on Minion. **Nobody has
claimed this passed** — that's what this run is for.

---

## One Minion session — this gate, then the TUI verify pass

This gate and the v0.6.x TUI live-verifies all need the same scarce thing — a real macOS host
with a running daemon/TUI, which can't exist inside a dev island — so do them in **one Minion
session**:

1. **This doc (P0 — the Show HN blocker):** the clean-Mac install/uninstall/re-adopt gate, below.
2. **Then, same session:** the consolidated **TUI verify pass** in
   [`v0.6.8-verify.md`](./v0.6.8-verify.md) — usage render (#167), name-collision UX (#168),
   wake-on-message, tab titles, used-counter, visual identity (it supersedes
   `v0.6.1-tui-verify.md`; **ping a2 with results** and a2 relays to the owner). Those are
   **lower-priority polish eyeballs, not launch gates** — a red there is a bug to file, not a
   Show HN stop.

The canonical per-item TUI checklist lives in that doc; this doc stays focused on the install
gate so the two don't drift.

---

## What it proves (per channel)

For EACH install channel the loop runs the full round-trip:

```
teardown (virgin)  →  install  →  assert daemon up + a test island running
    →  write a workspace marker  →  dejima uninstall --keep-islands
    →  assert the named volume + ~/.dejima config SURVIVE
    →  reinstall  →  assert the island + its marker RE-ADOPT (same name → same volume)
```

It **assembles**, it does not rebuild:
- `scripts/install-channels-check.sh` — the 21-assertion channel-consistency gate, run once
  up front (no Docker / no Mac needed; catches a broken install path first).
- the re-adopt assertion set mirrors `scripts/integration.sh`'s *"uninstall --keep-islands +
  re-adopt"* feature — the same volume-survives / marker-survives / re-adopt checks, but
  across a **real channel install + uninstall** instead of one in-process `$HOME`.

### Per-channel semantics (these differ by design — see `docs/distribution.md`)

| Channel | What installs | Round-trip in the gate |
|---|---|---|
| `curl \| sh` (`install.sh`) | **Full host**: clones source, builds `dejima` + `dejimad`, builds the island image, registers the daemon | Full teardown→install→island→`uninstall --keep-islands`→reinstall→re-adopt |
| `brew` (`brew install --HEAD`) | Builds **both** `dejima` + `dejimad` from source off `master` | Full round-trip (see caveat) |
| `npm` (`npm install -g dejima`) | **CLI client only** — the daemon is Unix-host-only | Installs the client + confirms it drives a daemon a source install stood up (no daemon round-trip) |

**Caveat — `brew --HEAD`:** it builds the two binaries but does **not** build the island
Docker image and does **not** register a launchd service. The harness handles both: the
test's `dejima init` builds the island image on first use, and the harness starts `dejimad`
in the foreground for the test. So the `brew` channel proves the *binary* install + the
re-adopt round-trip; the *service/launchd* path is the separate Tier-3 `service install`
test (`scripts/tier3/system.sh`, opt-in in `nightly.yml`). The pinned-binary formula
(`brew install aoos/dejima/dejima`, CLI only) is smoked separately and **skips** cleanly
until the `aoos/homebrew-dejima` tap + a published release back it (see `docs/distribution.md`).

**Caveat — `npm`:** the public package needs `NPM_TOKEN` set so a tag publishes (see
`docs/distribution.md`). Until then the `npm install -g dejima` step may fail to fetch a
prebuilt binary; the gate reports that channel red/skip rather than silently passing.

---

## How to run it on Minion

### Preconditions (one-time; see `docs/testing/dejimaqa-runner-setup.md`)
- Run as the **`dejimaqa`** test user — **NEVER `aoos`**. The teardown deletes `~/.dejima`
  and `~/.dejima-src`; pointing it at the operator account would wipe real islands. The
  harness hard-refuses to run as `aoos` (override only on a throwaway box with
  `CLEANMAC_ALLOW_AOOS=1`).
- `dejimaqa`'s own **colima** Docker is up: `colima start --cpu 4 --memory 8 --disk 60`,
  then `docker ps` answers. (Right-size / stop `aoos`'s colima first if the mini is tight —
  RAM note #23.)
- `go`, `git`, `curl` on PATH. `brew`/`npm` present if you want those channels (they skip
  cleanly if absent).

### Option A — via GitHub Actions (the button)
Actions → **`nightly-live`** → **Run workflow**. Inputs:
- `channels` (default `curl,brew,npm`) — comma-separated subset to run.
- `use_served` (default off) — when **on**, curls the production
  `https://dejima.tech/install.sh`; when **off**, runs the in-repo `install.sh` so the gate
  verifies the **branch under review**.
- `reset_colima` (default off) — also reset the `dejimaqa` colima VM to a from-zero Docker
  (destructive to its containers; teardown already removes Dejima's own objects, so leave
  off unless you want a truly-from-zero VM).

The job runs on the `[self-hosted, macos-mini]` runner, captures the full transcript +
per-channel daemon logs as the **`clean-mac-gate-logs`** artifact, and **fails loud** if any
assertion fails. It is `workflow_dispatch` only — there is intentionally **no nightly cron**;
turning on unattended auto-runs is your explicit decision (uncomment the `schedule:` block in
`.github/workflows/nightly-live.yml`).

### Option B — directly on the box (as `dejimaqa`)
```bash
# from a checkout of the branch/tag under test, as the dejimaqa user:
scripts/clean-mac/proof-loop.sh                      # all channels: curl,brew,npm
scripts/clean-mac/proof-loop.sh --channels curl      # one channel
CLEANMAC_USE_SERVED=1 scripts/clean-mac/proof-loop.sh --channels curl   # production curl path
scripts/clean-mac/proof-loop.sh --reset-colima       # also reset colima to from-zero
```
Teardown alone (reset the box to virgin), e.g. between manual experiments:
```bash
scripts/clean-mac/teardown.sh --assert-virgin        # idempotent; confirms nothing remains
scripts/clean-mac/teardown.sh --reset-colima         # + fresh colima VM (destructive)
```

### Knobs (env)
- `DEJIMA_PREFIX` (default `~/.local`) — where the harness installs the binaries. **Never
  `/usr/local`**, so the run is sudo-free and can't clobber an operator install.
- `CLEANMAC_USE_SERVED=1` — curl the served `install.sh` instead of the in-repo copy.
- `CLEANMAC_INSTALL_URL` — override the served install URL.
- `CLEANMAC_RESET_COLIMA=1` (+ `CLEANMAC_COLIMA_CPU/MEM/DISK`) — from-zero colima VM.
- `DEJIMA_REF` (default `master`) — git ref `install.sh` checks out.

---

## What GREEN looks like

The run ends with:
```
──────── clean-mac-launch-gate ────────
passed: N   failed: 0   skipped: K
clean-mac-launch-gate GREEN
```
and per channel you should see green (`✓`) for, at minimum:
- `STEP 0` install-channel consistency check.
- For `curl|sh` and `brew --HEAD`:
  - virgin teardown + `assert_virgin` (no dejima binary, no `~/.dejima`, no `~/.dejima-src`,
    no test volume).
  - install succeeded; `dejima --version` prints a **real version**, daemon up.
  - test island created; workspace marker written; island running (exec works).
  - **bare `uninstall` REFUSED** (no destructive default).
  - `uninstall --keep-islands` → **named volume SURVIVED** + **`~/.dejima` config SURVIVED**
    + the marker is readable from the kept volume **with no daemon running**.
  - reinstall → **RE-ADOPTED**: the workspace survived the uninstall→reinstall round-trip.
- For `npm`: the client installs, reports a version, and drives the running daemon (`dejima ls`).

`skipped` (`∼`) is **not** a failure — a channel skips cleanly when its tool is absent
(`brew`/`npm` not installed) or its backing isn't live yet (no tap / `NPM_TOKEN`). A real
assertion failure (`✗`) makes the run **red** and the workflow job fail; read the
`clean-mac-gate-logs` artifact (or the saved `~/cleanmac-<channel>-dejimad.log`) to diagnose.

When this run is green for all three channels on Minion, flip the §19 launch-gate rows in
`docs/testing/test-coverage-matrix.md` from `A†` (wired) to verified, and tick the install +
uninstall-safety rows in `docs/launch-checklist.md`.

---

## Relationship to the other proofs
- `docs/operator-tests/uninstall-keep-islands-readopt.md` — the **manual** virgin-Mac
  walkthrough of the same re-adopt guarantee (do this by hand once if you want to watch each
  step). This gate **automates** it across all three channels.
- `scripts/integration.sh` — the in-island Tier-2 version of the re-adopt round-trip (one
  `$HOME`, real Docker). This gate is the real-channel, real-uninstall superset.
