# Runbook — OpenClaw Home Island smoke test on Minion (macOS)

**Status:** Runbook for operator execution on Minion (macOS daemon host). Author cannot run it
from inside an island (no host Docker access); this is the script to drive it and the two
investigations to record.
**Last updated:** 2026-06-12
**Companion to:** `docs/port-island-spec.md` (esp. §3.2 Home Island, §6 V1, §11.3 macOS TCP path).

## Goal

Convert "the Port plumbing exists and passed an isolated test" into "a real assistant framework
ran inside a contained Home Island on Minion and read a brokered, audited host file." And while
doing it, **record two things that block the production value prop**:

1. **UID / permission mapping** — does the file `docker cp` puts into the island land readable by
   the in-island agent user (`dejima`, UID 1000) given macOS host ownership (UID ~501)?
2. **TCP loopback footprint** — the exact, minimal change set to let the *brain inside the island*
   call `dejimad` itself (autonomy), since the unix-socket path is Linux-only and Minion is macOS.

### What this proves / does not prove

- **Proves:** a framework daemon boots and runs as a Home Island; a read-only scope is granted;
  intake copies a host file in; the in-island runtime can read it; every crossing is ledgered and
  the chain verifies. (Watch-item #1 is *runtime-agnostic*: OpenClaw's file read is the same
  `open()/read()` syscall as `cat`, so `dejima exec … cat` is a faithful proxy for the permission
  behavior.)
- **Does not prove:** autonomous (brain-driven) operation on macOS — that is exactly what
  watch-item #2 scopes and is **not** unblocked by this test; the test only *measures the footprint*
  to unblock it.

---

## 0. Prerequisites (on Minion)

- Docker Desktop / OrbStack running; `dejimad` installed and the island image built
  (`dejima image build` or it auto-builds on first `init`).
- This runbook uses your **real** daemon (not the isolated test HOME) so the ledger and islands
  are inspectable afterward. The island name `oc-home` should not collide with an existing island.
- Node is present in the island image (claude-code is npm-based), so OpenClaw installs in-island.

---

## 1. Stand up the Home Island running OpenClaw

OpenClaw's full multi-channel onboarding needs secrets/channels we don't need for a read test, so
boot it as a long-lived daemon that installs OpenClaw and idles; we exercise the read explicitly.

```bash
# A throwaway "brain config" repo (the Home Island still needs a repo in V1).
mkdir -p /tmp/oc-config && (cd /tmp/oc-config && git init -q && git commit -qm init --allow-empty)

dejima home create \
  --name oc-home \
  --repo /tmp/oc-config --local-copy \
  --cmd 'sh -lc "npm install -g openclaw >/tmp/oc-install.log 2>&1; openclaw --version; sleep infinity"'

dejima logs oc-home --follow     # watch install; Ctrl-C once you see the openclaw version
```

Confirm the framework actually runs as the island's agent user:

```bash
dejima exec oc-home -- id                 # expect uid=1000(dejima)
dejima exec oc-home -- openclaw --version # expect a version, run AS dejima
```

> If `--explain-native` is relevant to your real deployment (OpenClaw wanting macOS
> Shortcuts/iMessage), see `port-island-spec.md` §3.3 — that path runs native, not here.

---

## 2. Grant a read-only scope and intake a file

```bash
# Build a tiny "vault" with a normal-mode note and a locked-down (0600) note,
# to surface the permission behavior in watch-item #1.
mkdir -p /tmp/vault
printf 'open note\n'   > /tmp/vault/open.md   && chmod 644 /tmp/vault/open.md
printf 'secret note\n' > /tmp/vault/locked.md && chmod 600 /tmp/vault/locked.md

dejima port grant oc-home /tmp/vault:ro
dejima port list  oc-home

dejima port intake oc-home vault:open.md
dejima port intake oc-home vault:locked.md
```

---

## 3. ⚠️ Watch-item #1 — UID / permission mapping (RECORD RESULTS)

**Why this is a real risk.** `docker cp` is a tar copy: it preserves the source file's **numeric
UID/GID and mode**. On macOS the host user is UID ~501; the in-island agent is `dejima` UID 1000.
`docker cp` does **not** use Docker Desktop's virtiofs/uid-squash (that's only for bind mounts), so
the file lands owned by a bare, unmapped numeric UID with the host's original mode. The intake
handler today does **not** chmod/chown after the copy.

**Observe (record the actual output):**

```bash
dejima exec oc-home -- ls -ln /home/dejima/intake/vault/    # numeric owner UID + mode of each file
dejima exec oc-home -- stat -c '%u %a %n' /home/dejima/intake/vault/open.md /home/dejima/intake/vault/locked.md
dejima exec oc-home -- cat  /home/dejima/intake/vault/open.md     # expect: succeeds
dejima exec oc-home -- cat  /home/dejima/intake/vault/locked.md   # expect: PERMISSION DENIED
```

**Expected anomaly / pass-fail:**

| File (host mode) | Lands as | `dejima` (1000) can read? |
|---|---|---|
| `open.md` (0644) | owner UID ~501, mode 0644 | ✅ yes — the `others` read bit saves it |
| `locked.md` (0600) | owner UID ~501, mode 0600 | ❌ **EACCES** — only UID 501 may read, dejima is 1000 |

> **CONFIRMED on Minion (2026-06-12) and FIXED.** The split above was exactly what happened —
> `open.md` read, `locked.md` EACCES (both owned by host uid 501). The anomaly: brokered
> readability depended on the host file's *mode*, not the broker's intent; 0600 files (private
> notes, keys, exported files) were silently unreadable.
>
> **Read-normalization is now implemented.** `handlePortIntake` copies through a daemon-owned temp
> set 0644 before `docker cp`, so the agent can always read what the broker chose to share —
> regardless of the host file's owner/mode. (Done host-side on a temp we own, because the agent
> isn't the file's owner and couldn't `chmod` it itself.) Rationale: the file is already *inside*
> the containment boundary, so in-island read bits add no exposure. Covered by a 0600-file
> assertion in `scripts/integration.sh`. After this fix, **both files read.**

**Also note (cosmetic):** macOS files may carry xattrs/resource forks; `docker cp` may emit a
benign warning. Record it if seen, but it does not block reads.

---

## 4. Ledger verification

```bash
dejima audit                 # expect port.grant + two trade.read rows (open.md, locked.md*)
dejima audit --verify        # expect: "ledger OK … hash chain intact", exit 0
```

\* Note whether a *failed-read intake* (locked.md) still ledgered a `trade.read`. It will —
intake ledgers the **crossing** (the bytes were copied in) before the agent ever reads them; the
EACCES happens later, at the agent's `open()`, which is outside the broker. That's correct: the
Ledger records what the broker moved, not what the agent could subsequently open. Worth stating in
the findings so the audit trail isn't misread.

---

## 5. ⚠️ Watch-item #2 — the TCP loopback footprint for autonomy (RECORD + DECIDE)

**The problem, precisely.** Host-driven trades (you running `dejima port intake …` in §2) work on
macOS today. **Brain-driven** trades — OpenClaw deciding *on its own* to fetch context — need the
brain inside the island to call `dejimad`. The in-island handle is the bind-mounted unix socket at
`/run/dejima/dejimad.sock`, which **is only mounted on Linux hosts** (Docker Desktop/colima cannot
bind-mount a unix socket over virtiofs). So on Minion the brain has no way to reach `dejimad`.
Until it does, **autonomy is blocked on your primary server** — a broken value prop for a 24/7
assistant, not an optional milestone.

**Two things are required, and the current code provides neither for a container:**

1. **Reachability** — the container must be able to dial the daemon. On Docker Desktop/macOS the
   route is `host.docker.internal` → the host. **Empirically verify this first** (the whole
   unblock hinges on it):

   ```bash
   # Start dejimad with a loopback TCP listener for the probe (see footnote on the flag),
   # then from inside the island:
   dejima exec oc-home -- sh -lc 'curl -sS -m 3 http://host.docker.internal:7273/v1/healthz; echo " rc=$?"'
   ```
   Record whether `host.docker.internal:<port>` reaches a daemon bound to the host's `127.0.0.1`.
   On Docker Desktop Mac it generally does (gvproxy forwards host.docker.internal to host
   loopback); **confirm it on Minion's exact Docker runtime** — if not, the daemon must instead
   bind an address the VM forwards (the docker bridge gateway), which the runbook step will reveal.

2. **Authorization** — and this is the real gap. The existing TCP listener
   (`cmd/dejimad/main.go` `tailscaleListener`) **only accepts tailnet source IPs and has no
   token**. A container arriving via `host.docker.internal` comes from the bridge gateway IP, which
   is **not** a tailnet IP → **rejected**. And the unix socket has **no per-island authorization at
   all** (full trust). So we cannot reuse either path: opening TCP to a container *requires a new,
   token-based, island-scoped auth*, or a compromised brain could drive the whole host.

### Minimal architectural footprint (the change set to unblock)

Smallest design that gives Minion autonomy without weakening containment:

1. **Per-island token.** Mint a random secret at provision; store host-side
   (`~/.dejima/projects/<name>/token`, 0600). One token ⇒ one island.
2. **Inject into the container.** At `createContainerForProject`, for Home Islands (or any island
   that needs autonomy), set env: `DEJIMA_HOST=host.docker.internal:<port>` and
   `DEJIMA_TOKEN=<secret>`. (Linux keeps using the socket; this is the macOS/no-socket path.)
3. **Token-authenticated listener.** Add a listener/middleware bound to a **host-internal address
   only** (loopback, reachable via host.docker.internal — never `0.0.0.0` on the LAN) that accepts
   `Authorization: Bearer <token>` with a **constant-time compare**, as an alternative to the
   tailnet-IP check. Binding limits exposure; the token is the actual auth.
4. **Island-scoped authorization.** The middleware resolves token → island and **rejects any
   request whose `{name}` ≠ the token's island** for island-scoped routes (port/intake/exec/etc.).
   This is net-new (today there is no such scoping) and is the security crux: it's what stops a
   compromised Home Island brain from touching another island or the host.
5. **In-island client wiring.** The in-island `dejima` CLI's `client()` already supports a TCP
   client (`NewTCPClient`) and reads `DEJIMA_HOST`; add bearer-token support so
   `dejima port intake …` *run by the brain inside the island* authenticates automatically. The
   brain then just shells out to the CLI — no new SDK.

**The one real decision to make (don't hand-wave):** spawning. A Home Island's job includes
spawning **Project Islands**, which is an island-*create*, not an action on its own island — so the
token needs a capability beyond "act on self." Proposed model: a Home-Island token grants
(a) full Port/exec on its own island and (b) `island.create`; and **`create` returns the child's
token**, which the brain holds to drive the child. That keeps every island individually scoped (no
god-token) while enabling the spawn-and-drive workflow. Flag this for a decision before building —
it's the difference between "autonomy" and "a token that can do anything."

### Footprint summary (files touched)

| Concern | File |
|---|---|
| Token mint + store | `internal/project` (or a `~/.dejima/projects/<n>/token` helper) + `provision` |
| Env injection (`DEJIMA_HOST`, `DEJIMA_TOKEN`) | `internal/api/server.go` `createContainerForProject` |
| Token listener (host-internal bind) | `cmd/dejimad/main.go` (alongside `tailscaleListener`) |
| Bearer-token auth + island-scoping middleware | `internal/api/server.go` `Handler()` |
| In-island TCP client w/ token | `internal/api/client.go`, `cmd/dejima` `client()` |
| Spawn-returns-child-token | `internal/api/server.go` `createIsland` |

This is a **bounded, ~1–2 day** change — small enough that "autonomy on Minion" should be the next
build after this smoke test, not a someday item. It graduates `port-island-spec.md` §11.3 from
open question to spec.

---

## 6. Teardown

```bash
dejima purge oc-home -f
rm -rf /tmp/vault /tmp/oc-config
# (the ledger at ~/.dejima/ledger.jsonl persists — that's the point; inspect or archive it)
```

---

## 7. Findings to capture (paste back)

1. **#1 UID/perms:** the `ls -ln` + `stat` output; did `open.md` read and `locked.md` EACCES as
   predicted? Any xattr warnings? → decides whether we ship the `chmod a+r`-on-intake fix.
2. **#2 reachability:** did `host.docker.internal:<port>/v1/healthz` reach a loopback-bound daemon
   on Minion's Docker runtime? → decides bind address for the token listener.
3. **OpenClaw boot:** did `openclaw --version` run cleanly as `dejima`? Any node/permission gripes
   from the install log (`/tmp/oc-install.log` inside the island)?

With #1 and #2 answered from a real run, the two follow-on builds (intake read-normalization; the
token-scoped TCP path for autonomy) are fully specified.

---

### Footnote — running dejimad with a loopback TCP listener for the §5 probe

The current `--tcp` flag attaches the **tailnet-only** listener, which will reject the container.
For the *probe* only, you're testing raw reachability to a TCP port, so a quick way to check
host.docker.internal routing is to bind any throwaway listener on the host loopback (e.g.
`python3 -m http.server 7273 --bind 127.0.0.1`) and curl it from inside the island per §5.1. Do
**not** point the real tailnet `--tcp` at the container and expect it to work — it won't, by
design, until the token listener above exists.
