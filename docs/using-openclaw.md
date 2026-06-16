# Using OpenClaw with Dejima

This is the operator's guide to running an OpenClaw assistant brain on Dejima:
how to create it, configure it, give it access to your files, let it work
autonomously, and verify it's healthy. The same flow applies to other headless
assistant frameworks (Hermes, Letta) — OpenClaw is the worked example.

> TL;DR
>
> ```bash
> dejima home create --name jarvis --repo <brain-config-repo> --cmd "openclaw gateway"
> dejima home configure jarvis --scaffold      # stop it idling unconfigured
> dejima port grant jarvis ~/vault:ro          # the only host-file window
> dejima home doctor jarvis                     # is it ready + does it have access?
> dejima logs jarvis --follow                   # watch it work
> ```

---

## 1. What a Home Island is

A **Home Island** is a persistent island that runs an always-on assistant brain
(OpenClaw's `gateway`) *inside containment* instead of native on your host.

Why contained? The brain reads **untrusted inbound channels** — chat, email,
model output. That is the highest prompt-injection surface there is. If the brain
ran native with your `$HOME` mounted, a single injected message could read or
exfiltrate anything you can touch. In a Home Island it can't: it reaches your
files only through the **Port** (scoped + ledgered, below) and creates work
islands only through the daemon API. An injected prompt is contained to the
scopes *you* granted, and every file crossing is in a tamper-evident log.

Run the brain native only when it genuinely needs host-OS APIs a container can't
reach through a file broker (macOS Shortcuts, Apple Notes, iMessage). See
`dejima home create --explain-native`. For files + code, always prefer a Home
Island.

## 2. Prerequisites

- The daemon (`dejimad`) is running and healthy — check `dejima doctor`.
- The island image is built (`make image`, or it auto-builds on first create).
- **For brain-driven autonomy** (the brain deciding *on its own* to fetch a file
  or spawn an island):
  - **Linux daemon host:** works out of the box — the in-island unix socket is
    bind-mounted.
  - **macOS daemon host:** the unix socket can't be mounted, so start the daemon
    with the token listener:
    `dejimad --token-tcp 127.0.0.1:7274` (or
    `dejima service install --token-tcp 127.0.0.1:7274`).
  - Confirm with `dejima doctor` or `dejima home doctor <name>` — the **autonomy**
    row tells you whether it's live.

## 3. Create your Home Island

```bash
dejima home create \
  --name jarvis \
  --repo git@github.com:you/brain-config.git \
  --cmd "openclaw gateway"
```

- `--repo` is the brain's **config/workspace** repo — it becomes `/workspace`
  inside the island and persists across restarts. Use `--local-copy` with a local
  path to seed from your working copy instead of cloning origin.
- `--cmd` is the brain's launch. The `openclaw` agent type also has a baked launch
  that npm-installs OpenClaw on first boot, so the gateway comes up even on a fresh
  image.
- On first boot OpenClaw runs `--allow-unconfigured`: it installs, then **idles**
  waiting for config. That's expected — step 4 fixes it.

`home create` prints ordered next-steps and an **autonomy** status line so you
know immediately whether brain-driven Port/spawn will work on this host.

## 4. Configure OpenClaw

Until there's config in the workspace, the gateway just idles. Scaffold a
starting point:

```bash
dejima home configure jarvis --scaffold
```

This writes — **into the island's `/workspace`, never your host** —
`openclaw.config.toml`, a `.gitignore`, and `SECRETS.md`, then best-effort
reloads the brain (it relies on the headless restart loop, so no state is lost).
Then:

1. Edit `/workspace/openclaw.config.toml` (via `dejima connect`/`exec`, or in your
   editor over the SSH façade) to enable the channels you want.
2. **Commit it to the brain's config repo** so it survives a rebuild.
3. Re-run `dejima home configure jarvis --scaffold --force` only if you want to
   reset the scaffold; otherwise existing files are left untouched.

## 5. Channel credentials (secrets)

Secrets are **never** scaffolded to disk in plaintext and never committed to the
config repo — they're the keys to the accounts the brain reads. Two safe paths
(both also documented in the scaffolded `/workspace/SECRETS.md`):

**a. Set them inside the island** (ephemeral, lives in the home volume, not git):

```bash
dejima exec jarvis -- openclaw secrets set slack <token>
```

**b. Broker a creds file through the Port** (audited — every crossing is
ledgered):

```bash
dejima port grant  jarvis ~/brain-secrets:ro
dejima port intake jarvis brain-secrets:creds.env
# brain reads /home/dejima/intake/brain-secrets/creds.env
```

## 6. Granting access to your files (the Port)

The brain has **no raw host access**. The control socket is never mounted, and
the default is **deny-all**: a fresh Home Island can reach *none* of your files.
You open windows explicitly:

```bash
dejima port grant jarvis ~/vault:ro       # read-only scope named "vault"
dejima port grant jarvis ~/projects:ro
dejima port list  jarvis                    # what's granted
dejima port revoke jarvis vault             # close a window
```

Files move through the broker, not a live mount:

- `dejima port intake <island> <scope>:<path>` — copy a host file *into* the
  island (read).
- `dejima port export <island> <path>` — copy a file *out* to host staging.
- `dejima port write   <island> <scope> …` — write back into an `:rw` scope.

Every grant, revoke, and trade is appended to a **hash-chained, tamper-evident
ledger**:

```bash
dejima audit              # list crossings
dejima audit --verify     # walk the chain; non-zero exit if tampered
```

**Granting is operator-only by design.** The in-island token can `intake`/`write`
*within* scopes you've already granted, but it **cannot** `port grant`/`revoke` —
so a compromised brain can never widen its own access. The human grant is the only
widening path.

## 7. How the brain works autonomously

Once autonomy is live (§2), every container is provisioned with `DEJIMA_HOST` and
its own per-island `DEJIMA_TOKEN`. The brain just shells out to the `dejima` CLI
inside the island; the CLI authenticates with the token automatically. So the
brain can, on its own:

- `dejima port intake …` — pull context from a granted scope.
- create a **Project Island** via the API to do a coding task — and the create
  response returns *that child's* token, which the brain holds to drive it.

Each island is individually token-scoped: a Home Island's token works on its own
island plus `island.create`; it can't touch another island or the host. No
god-token.

## 8. Verify readiness

One command answers "is it ready and does it have what it needs?":

```bash
dejima home doctor jarvis
```

| Row | Means |
|---|---|
| `container` | the island is running |
| `agent` | the brain process is alive (a live container can still hold a dead brain) |
| `autonomy` | brain-driven Port/spawn is live — and on a running island it *probes* the in-island dial to the daemon |
| `port scopes` | how many host-file windows are open (WARN if none — deny-all) |
| `config` | `/workspace/openclaw.config.toml` exists (else the brain idles) |
| `channel creds` | reminder of the two safe ways to supply secrets |

A `FAIL` exits non-zero, so it composes with scripts/CI.

## 9. Watch & operate

```bash
dejima logs jarvis --follow            # stream the brain's output
dejima exec jarvis -- openclaw --version
dejima status jarvis                   # state, health, attached clients
dejima connect jarvis                  # attach a shell/agent session
```

OpenClaw is headless (non-attachable), so you read it via `logs` and drive it via
`exec` rather than `connect`-ing to its session.

## 10. Security model & boundaries

What the brain **cannot** do, even if prompt-injected:

- Widen its own file access — only the operator's `port grant` does that.
- Reach the host filesystem outside granted scopes, or via a raw mount (there is
  none).
- Touch another island or the daemon's control plane — the token is island-scoped,
  default-deny.
- Hide its tracks — every file crossing is in the verifiable ledger.

Going native (`--explain-native`) trades this containment for host-OS reach; do it
deliberately and only for capabilities a file broker can't provide.

## 11. Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| Brain does nothing after create | Unconfigured — `dejima home configure <name> --scaffold`, then fill in config. |
| `home doctor` autonomy = OFF | macOS daemon without the token listener — `dejimad --token-tcp 127.0.0.1:7274`. |
| autonomy enabled but probe FAILs | `host.docker.internal` isn't the right route on this Docker runtime — set the daemon's `--autonomy-dial`. |
| `intake` of a 0600 file unreadable in-island | Already fixed: intake read-normalizes through a daemon-owned temp; update the daemon. |
| Brain can't see a file you expect | No scope granted (deny-all) — `dejima port grant <name> <path>:ro`. |
| Install errors on first boot | `dejima logs <name>` / `dejima exec <name> -- cat /tmp/oc-install.log`. |

## 12. Teardown

```bash
dejima purge jarvis            # destroy the island + volumes (guards unpushed work)
```

The ledger at `~/.dejima/ledger.jsonl` **persists** on purpose — it's the audit
record of everything the brain ever read or wrote. Archive it, don't expect purge
to clear it.

---

See also: `docs/port-island-spec.md` (Port + ledger design, §3.2 Home Island),
`docs/runbook-openclaw-home-island.md` (operator smoke test), and
`dejima home --help`.
