# Mac mini host setup

> The read-it-yourself companion to `dejima onboard --provision-host`. If you'd
> rather be walked through it, just run that command — it does everything below
> automatically where it can, and tells you what needs a click. This page is for
> people who want to understand (or do) each step by hand.

**Goal:** a fresh Mac mini → a secure, always-on personal AI-agent server, reachable
from your laptop/phone over Tailscale, in ~15–20 minutes.

The fast path:

```bash
# On the Mac mini:
go install github.com/aoos/dejima/cmd/dejima@latest   # or build from source (see below)
dejima onboard --provision-host
```

`--provision-host` is **macOS-only** and **resumable** — if you quit partway (e.g.
while Docker Desktop finishes installing), re-run it and it picks up where it left
off. `--yes` runs it non-interactively (auto-doing the scriptable steps and
printing a checklist of the GUI-only ones); `--reset` starts the provisioning over.

The wizard runs six phases, each *detect → act → verify*. Here's what each does and
how to do it by hand.

---

## 1. Power & system settings (the critical one)

A Mac mini that sleeps drops every attached session and stops every agent. This is
the single most important setting.

```bash
sudo pmset -a sleep 0 disablesleep 1 womp 1 autorestart 1
```

- `sleep 0` / `disablesleep 1` — never sleep.
- `womp 1` — wake for network access.
- `autorestart 1` — restart automatically after a power failure.

Verify with `pmset -g | grep sleep` (should read `sleep 0`).

**Auto-login** (GUI-only — the wizard can't do this for you, because it stores your
account password): *System Settings → Users & Groups → Automatically log in as →
\<your user\>*. This lets the daemon come back after an unattended reboot. It's
optional if you install the daemon as a `--system` LaunchDaemon (which starts at
boot before any login — the wizard does this by default).

## 2. Tooling — Homebrew, Tailscale, Docker

```bash
# Homebrew (the package manager for the rest):
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# Tailscale (the private network your other devices use to reach this host):
brew install --cask tailscale-app
tailscale up --ssh --accept-dns=true     # opens a browser to log in

# Docker (the container engine islands run in):
brew install --cask docker-desktop
open /Applications/Docker.app            # launch once to grant permissions + start the engine
```

Verify: `tailscale status` shows *Running*; `docker version` reaches a server.

Optional: `brew install gh && gh auth login` for per-island GitHub identities.

## 3. Docker VM memory

Docker's Linux VM defaults small (≈2 GB), and **all** islands share that one pool —
so an undersized VM means islands get OOM-killed no matter what per-island limits
you set. Right-size it:

```bash
dejima doctor --fix      # scripts the colima resize, or tells you the Docker Desktop setting
```

Rule of thumb: ~¾ of host RAM, leaving the host at least 4 GiB.

## 4. Shell PATH & Remote Login

Non-interactive SSH sessions don't load your interactive shell, so put Homebrew on
the PATH where they'll see it:

```bash
echo 'export PATH="/opt/homebrew/bin:$PATH"' >> ~/.zshenv
```

Enable Remote Login so VS Code / Cursor Remote-SSH (and `sftp`) can reach islands:

```bash
sudo systemsetup -setremotelogin on
```

## 5. Install the Dejima daemon

Install `dejimad` as a **system** service so it starts at boot with no login, with
the recommended host posture baked in:

```bash
dejima service install --system \
  --tcp :7273 \
  --token-tcp 127.0.0.1:7274 \
  --audit
```

- `--tcp :7273` — remote clients reach the daemon over the tailnet (tailnet peers
  only; off-tailnet connections are refused).
- `--token-tcp 127.0.0.1:7274` — the in-island autonomy path.
- `--audit` — the operational audit log (API requests + lifecycle events on the
  tamper-evident ledger). See [`audit.md`](audit.md).

If you don't have the `dejimad` binary yet (e.g. you installed only the client with
`go install`), build the full stack from source first:

```bash
git clone https://github.com/aoos/dejima.git ~/code/dejima
cd ~/code/dejima && make setup
```

## 6. Verify & connect

```bash
dejima doctor        # full health check
dejima               # the dashboard
```

From another device on the **same Tailscale account**:

```bash
go install github.com/aoos/dejima/cmd/dejima@latest
export DEJIMA_HOST=<this-host>.tailnet.ts.net:7273
dejima ls
```

`dejima onboard --provision-host` also drops a `dejima-quick-reference.txt` on your
Desktop with these connection details filled in.

---

## Security model

- **Tailnet-only by default.** Remote access is pinned to your Tailscale network —
  no port-forwarding, no public IP. The TCP listener accepts only tailnet peer IPs.
- **Container isolation.** Agents run inside islands (containers); the host and
  other islands are out of reach. The exchange-down token boundary
  ([`security-boundary.md`](security-boundary.md)) means an island only ever holds
  an attenuated, per-island token — never your master identity.
- **Audit.** With `--audit`, operator actions and API requests are recorded on a
  hash-chained ledger you can read/verify with `dejima audit`.

This is not a hardening framework — it sets sane defaults and offers a couple of
opt-in steps. Tailscale + container isolation is the model.

## Troubleshooting

- **Can't reach the host from a laptop?** Run `dejima` (or any command) on the
  client — if `DEJIMA_HOST` is set and the daemon is unreachable, the CLI offers a
  one-shot troubleshooter. Or run `dejima doctor`. The usual causes: not on the
  same tailnet, or the host's daemon isn't exposing TCP (`dejima service install
  --tcp :7273` on the host).
- **Islands getting OOM-killed?** The Docker VM is too small — see phase 3.
- **Daemon didn't come back after reboot?** Confirm either auto-login is on, or the
  daemon was installed with `--system` (a LaunchDaemon that loads before login).

## Notes

- macOS-only. A Linux host equivalent is a separate effort (different package
  managers and service manager).
- The wizard is resumable but not strictly idempotent in every order — it skips
  already-done steps via `~/.dejima/provisioning-state.json`; `--reset` clears it.
