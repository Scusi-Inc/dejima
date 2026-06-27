# Lane — island version-skew detection + idempotent shim refresh

You are the **Skew-detection** agent for Dejima. Motivating incident: an island built
from a pre-2026-06-13 image still shipped the OLD Unix-socket `notify.sh` agent-event hook,
which silently `exit 0`'d on a TCP-only island. Result: that island's agent-state heartbeat
NEVER fired — for 18h, with no signal anywhere — silently breaking mail-nudges, idle
auto-hibernate, and the agent-idle-seconds metric (they all read the same heartbeat). The
daemon/CLI were current; only the **island layer** was stale. Make this class of skew
**loud** (detect it) and make `dejima upgrade` actually **propagate** shim fixes.

## Reality check (verify first — confirmed 2026-06-24)

- **No version/build stamp on islands today.** `internal/project/project.go` records no
  image-id / daemon-version an island was built or last-upgraded against — so there is
  nothing to compare. #1 must ADD this stamp. (Confirm before building.)
- **Doctor check framework exists:** `cmd/dejima/doctor_checks.go` (`checkConnection`,
  `checkInstallMeta`, `checkStateOwnership`, `checkListenerExposure` — each takes
  `*doctorReport`). Add the skew check here.
- **`init.sh` already copies `notify.sh` UNCONDITIONALLY** (`image/agents/claude-code/init.sh:41`)
  — so the refresh path mostly works; the gaps are (a) the **image's** `/opt` copy being
  stale, and (b) `settings.json`'s hook block being written only "if not already configured"
  (`init.sh:47`). `dejima upgrade` (`upgradeIsland`, `server.go:2164`) recreates the
  container against the current image but PERSISTS both volumes.
- Heartbeat state lives in the daemon's `agentStates` map (`server.go`), keyed per
  (island, agent); `agent-idle-seconds` metric (Lane 0.6) already reads it.

## Deliverable 1 — make skew LOUD (highest value; ship first, own PR)

1. **Stamp islands** with the image build-id and/or daemon version they were created and
   last-upgraded against — a field on `project.Project` (persisted in `dejima.toml`), set at
   island create AND in `upgradeIsland`. Pick the most robust available identifier (image
   digest/tag/build-id; daemon `version.Version`). If no per-image build-id exists, add one
   at image-build time (`POST /v1/image/build` / `internal/islandimage`).
2. **Compare + surface:** a `dejima doctor` check (new fn in `doctor_checks.go`) and a
   signal in `dejima ls` / island detail that flags islands whose stamp is behind the
   daemon's current version. **#3-lite:** the check must print the EXACT remedy next to the
   finding, e.g. `island "x" built on v0.1.4, daemon on v0.5.3 — run: dejima upgrade x`.
3. **Zero-heartbeat liveness flag** (strong broken-shim signal): flag an island/agent that
   has emitted NO agent-state heartbeat since boot (read `agentStates`). This is what would
   have caught the incident directly.

## Deliverable 2 — idempotent managed-file refresh (ship after D1)

Make daemon-OWNED in-island files self-heal on every boot so `upgrade` propagates fixes:
1. `init.sh`: reconcile the **hook block** in `settings.json` idempotently — ensure the
   Notification/Stop → `dejima-notify.sh` wiring matches the current contract every boot
   (merge, don't clobber the user's other settings), instead of "only if absent". Keep
   `notify.sh`'s unconditional copy.
2. Ensure `dejima upgrade` recreates against a **freshly available** image (document/verify
   that an image rebuild + upgrade actually refreshes `/opt` shims; if upgrade silently
   reuses a stale cached image, surface that).
3. Treat the principle explicitly in a short doc: managed/daemon-owned files (hook scripts,
   hook wiring) are re-derived each boot; only user data is sticky.

**Out of scope (DEFER — do NOT build):** full `dejima upgrade --all` / host-update
auto-propagation. Bulk island recreation is disruptive and is a separate decision. D1's
detection + the remedy text is the safe slice.

**You own:** the version-stamp field + comparison (`internal/project`, island create +
`upgradeIsland`), the new `doctor_checks.go` check + `ls`/detail surfacing, the
zero-heartbeat signal (read `agentStates`), and `init.sh` settings.json reconciliation +
the doc. Tests. **Do NOT touch:** the wake/mailbox internals (`wake.go`, `mailbox.go`),
install/uninstall, the grant routes.

**Workflow:** Own worktree, branch `feat/p1-skew-detection`. Never `cd /workspace` or enter
another worktree. `go test ./...` + `golangci-lint run` (v2; master requires lint+build);
shell changes pass `shellcheck`. Commit only your own hunks; **ship D1 as its own PR first**
if D2 proves large. PR to `master` when green. Go 1.26.3.

**Done when:** an island built from an old image is flagged (with the exact `upgrade`
remedy) by `dejima doctor`/`ls`; a never-heartbeat island is flagged; and `init.sh`
idempotently reconciles the hook wiring so an upgrade self-heals the socket→TCP class of break.
