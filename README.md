<p align="center">
  <img src="assets/logo.png" alt="Dejima" width="180">
</p>

<h1 align="center">Dejima</h1>

<p align="center"><em>The substrate for multi-device agent workflows.</em></p>

---

**Dejima** is an OSS containment layer for autonomous AI coding agents. Each project runs in its own *island*: an isolated container with the target repo, the agent of your choice, and a strictly brokered *bridge* back to the host.

It is the interstitial layer between an agent and your machine. Not an interface. Not a product. Plumbing meant to be consumed by other applications — your CLI, a phone app, a Slack bot, a custom agent harness — all hitting the same API.

> The name and silhouette borrow from the historical island of Dejima in Nagasaki Bay — a single-bridge trading post, for two centuries the only sanctioned point of contact between Japan and the outside world. Same idea here.

## Status

Pre-alpha. v1 milestones M0–M5 are implemented; M6 (real-world dogfood on a Mac mini) is in progress. See [`docs/v1-spec.md`](docs/v1-spec.md) and [`docs/v1-milestones.md`](docs/v1-milestones.md).

## What you get

- **Isolation** — agents run inside containers with no path to your host filesystem, other projects, or unrelated secrets.
- **Multi-project, multi-agent** — N islands on one host, each its own repo and agent. No context bleed.
- **Persistent sessions** — long-running agent work survives disconnects and host reboots via tmux + named volumes.
- **Multi-device attach** — drive the same island from a laptop, phone, or web client. Shared screen, presence-aware.
- **Direct push to GitHub** — host credentials mounted read-only into the island; `git push` just works.
- **API-first** — the CLI is one client of the Dejima API. Mobile apps, Slack bots, custom integrations target the same surface.

## Setup at a glance

Dejima has two roles:

- **The host** (e.g. your Mac mini). Runs `dejimad` as a launchd/systemd service, owns the islands, holds the workspace volumes. Needs Docker (OrbStack). One per network.
- **Clients** (your laptop, eventually your phone). Just need the `dejima` CLI. Talk to the host via Unix socket locally or via a Tailscale-pinned TCP port remotely.

A first-time setup runs through these in order:

1. Install Docker on the host (OrbStack on macOS).
2. Install `dejima` + `dejimad` on the host; build the island image; install the service.
3. Optionally install `dejima` (just the CLI, no daemon, no Docker) on other devices.
4. `dejima init --repo …` on the host (or any client with `DEJIMA_HOST` set) to create an island.
5. `dejima connect <name>` from any tailnet device to attach.

## Install (host)

```bash
curl -fsSL https://aoos.github.io/dejima/install.sh | bash
```

That bootstraps everything: installs Homebrew prereqs (Go, Docker Desktop if needed), clones the source to `~/.dejima-src`, builds the `dejima` + `dejimad` binaries, installs them to `/usr/local/bin`, builds the island image, registers the daemon as a launchd (macOS) or systemd user (Linux) service, and runs `dejima doctor` to verify. Idempotent — re-run to update.

> The install URL above goes live the moment GitHub Pages is enabled on the repo (Settings → Pages → Branch: `master`, Folder: `/web`). Until then, fall back to the raw URL: `curl -fsSL https://raw.githubusercontent.com/aoos/dejima/master/web/install.sh | bash`. Once you've registered a custom domain (see [`docs/distribution.md`](docs/distribution.md)), `dejima.sh/install.sh` (or whichever TLD) becomes the canonical URL.

### One-liner environment knobs

```bash
# Auto-subscribe a push-notification webhook in the same step
curl -fsSL https://aoos.github.io/dejima/install.sh \
  | NOTIFY_URL=https://ntfy.sh/your-topic bash

# Non-interactive (auto-install Docker without the y/n prompt)
curl -fsSL https://aoos.github.io/dejima/install.sh \
  | AUTO_INSTALL_DOCKER=1 bash

# Install binaries to ~/.local/bin instead of /usr/local/bin (no sudo)
curl -fsSL https://aoos.github.io/dejima/install.sh \
  | PREFIX=$HOME/.local bash
```

### Via Homebrew (macOS, once the tap exists)

```bash
brew install --HEAD aoos/dejima/dejima
```

See [`docs/distribution.md`](docs/distribution.md) for how to create the `aoos/homebrew-dejima` tap. The formula in `homebrew/dejima.rb` is ready to drop in.

> v1 targets macOS (Mac mini priority) and Linux. Windows is not supported.
>
> **macOS**: the installer will offer to install [Docker Desktop](https://www.docker.com/products/docker-desktop/) via Homebrew if Docker isn't present. Docker Desktop is free for personal use and small businesses (<250 employees AND <$10M revenue). Faster alternatives if you prefer to install one yourself first: [OrbStack](https://orbstack.dev) (personal-license only) or [colima](https://github.com/abiosoft/colima) (CLI-only, OSS).
>
> **Linux**: install Docker engine natively from your distro (`docker.io` on Debian/Ubuntu, `docker` on Arch/Fedora) before running the installer. No desktop app needed.

## Install (client only)

To drive a remote Dejima host from your laptop, you only need the CLI — no daemon, no Docker. If you have Go installed:

```bash
go install github.com/aoos/dejima/cmd/dejima@latest
```

Otherwise the host installer above works for clients too (it'll build everything, but you can `make build && make install` directly to skip the Docker bits).

## Updating

Re-run the one-liner. The script does a `git pull` on the existing checkout and re-runs `make setup`. New binaries replace the old; existing islands and the daemon's persisted state are untouched.

## Uninstall

```bash
dejima service uninstall                          # stop the daemon
sudo rm /usr/local/bin/dejima /usr/local/bin/dejimad
rm -rf ~/.dejima-src                              # source checkout
# Optional: also remove islands and persisted state:
docker ps -aq --filter label=dejima.project | xargs -r docker rm -f
docker volume ls -q --filter label=dejima.project | xargs -r docker volume rm
docker network ls -q --filter name=dejima-net- | xargs -r docker network rm
rm -rf ~/.dejima
```

## Install paths under the hood

If you'd rather see exactly what the installer runs, the equivalent manual flow is:

```bash
git clone https://github.com/aoos/dejima.git ~/.dejima-src
cd ~/.dejima-src
make setup        # detect Docker → build → install → image → service → doctor
```

`make setup` itself is just `scripts/setup.sh`; the one-liner is `scripts/install.sh` which adds the prereq-bootstrap + checkout step in front of it. Either is fine.

## Future install paths (roadmap)

- **GitHub Releases** with prebuilt darwin/linux binaries (skip the Go-build step).
- **Homebrew tap** (`brew install aoos/dejima/dejima`).

### Install the daemon as a service

```bash
dejima service install     # writes a launchd plist (macOS) or systemd user unit (Linux)
dejima service status      # confirm it's loaded
```

The daemon now runs in the background, survives reboots, and is reachable at `~/.dejima/dejimad.sock`.

You can also subscribe a notification webhook in the same step (e.g. ntfy.sh push to your phone whenever an island event fires):

```bash
dejima service install --notify https://ntfy.sh/your-private-topic
```

### Verify the install

```bash
dejima doctor
```

Reports daemon / Docker / image / Tailscale / project / webhook health with actionable fix hints. Exits non-zero on failure so it composes with scripts.

## Quickstart

```bash
# Create an island around a real GitHub repo.
dejima init --repo git@github.com:you/myproject.git

# Connect — drops you into Claude Code, inside the island.
dejima connect myproject

# Inside the island, ask Claude Code to make a commit and push.
# The push lands on GitHub under your normal git identity.

# Disconnect with Ctrl-b then d (tmux). The session keeps running.

# See what's running.
dejima ls

# Pick up the session from another device on your tailnet:
DEJIMA_HOST=mac-mini.tailnet-name.ts.net:7273 dejima connect myproject

# Pause when memory pressure bites:
dejima hibernate myproject
dejima wake myproject

# Start a fresh conversation but keep the code:
dejima reset myproject

# When you're done:
dejima purge myproject
```

## Multi-device access

The daemon exposes its API on a Unix socket locally (no auth needed; filesystem permissions are the trust boundary) and, optionally, on a Tailscale-pinned TCP port for remote clients:

```bash
# Start the daemon with a TCP listener (only tailnet IPs are accepted).
dejimad --tcp :7273
```

From any other device on your tailnet, point the CLI at the host:

```bash
DEJIMA_HOST=mac-mini:7273 dejima ls
```

Multiple devices can attach to the same island session simultaneously. Each sees the same screen; presence lists who else is connected.

## Concepts

| Term     | Meaning                                                       |
|----------|---------------------------------------------------------------|
| Island   | The container holding a single project and a single agent.   |
| Bridge   | The brokered I/O channel between host and island.            |
| Trade    | A synced export of changes from island to host.              |
| Intake   | Files passed into the island via `dejima import`.            |
| Ledger   | (Post-v1) An audit log of every bridge transaction.          |

CLI verbs are intentionally functional; the metaphor lives in concept names, status output, and docs.

## CLI reference

```text
dejima init      Provision a new island.
dejima connect   Attach to an island's session (multi-attach).
dejima ls        List all islands.
dejima status    Detail view of a single island, including presence.
dejima hibernate Stop the container, preserve volumes.
dejima wake      Start a hibernated island.
dejima reset     Clear agent state. Preserves workspace.
dejima purge     Destroy island and volumes.
dejima exec      Run a one-shot command inside an island.
dejima cp        Copy files in or out of an island.
dejima logs      Tail an island's container logs (--follow).
dejima doctor    Health check: daemon, Docker, image, projects, networks, webhooks.
dejima service   Install / uninstall dejimad as a host service (--notify <url>).
dejima webhook   Subscribe a URL to receive state-change events.
```

Short alias: `alias dj=dejima` in your shell config if you want fewer keystrokes.

## Webhooks (for integrating from other apps)

```bash
# Get notified when an agent finishes a task or needs your input.
dejima webhook subscribe --url https://ntfy.sh/your-private-topic
```

Events fired by the daemon include lifecycle (`island.created`, `island.hibernated`, etc.), presence (`client.attached`, `client.detached`), and — when the Claude Code shim is active — agent-emitted events (`agent.waiting-for-input`, `agent.task-complete`).

This is the integration point for Slack / Discord / Telegram bots, mobile push, custom dashboards, etc. The Dejima daemon stays agnostic about what's on the other end of the URL.

## Architecture (one-paragraph)

`dejimad` is the host daemon. It manages island containers via the Docker API, exposes the [Dejima HTTP API](docs/v1-spec.md#the-dejima-api-concept) over a Unix socket (and optionally a Tailscale-pinned TCP port), and mediates PTY streams between clients and the in-container tmux session. Containers stay running across reboots; their volumes hold workspace and agent on-disk state. Per-agent shims (currently Claude Code) live in `image/agents/<agent>/` and provide opt-in conveniences without contaminating the agnostic core.

## Troubleshooting

| Symptom                                               | Fix                                                                                 |
|-------------------------------------------------------|-------------------------------------------------------------------------------------|
| `daemon unreachable: is dejimad running?`             | `dejima service status` or run `dejimad --foreground` for debug logs.               |
| `image dejima/island:latest not found locally`        | Run `make image`. The first build takes a few minutes.                              |
| `docker: command not found` / daemon not reachable    | Install Docker Desktop (or OrbStack / colima). On Linux: `sudo apt install docker.io` (or distro equivalent).|
| `gh auth setup-git failed`                            | `gh auth login` on the host; restart the island (`dejima reset <name>`).            |
| Agent can't push to GitHub                            | Make sure `~/.config/gh/` exists on the host. SSH-key flow: `dejima init --ssh-key`.|
| TCP listener: connection refused from off-tailnet     | This is intentional. Only tailnet IPs are accepted on the TCP listener.             |
| Memory creeping over time                             | `dejima hibernate <name>` then `dejima wake <name>` for a clean restart.            |

For more, see [`docs/v1-spec.md`](docs/v1-spec.md).

## Roadmap

See [`docs/roadmap.md`](docs/roadmap.md) for the full prioritized list. Highlights:

- **v1.x hardening** — container watchdog, `upgrade`, `panic`, credential refresh, Keychain-backed secrets, idle auto-hibernate.
- **v2** — trust-on-first-use for new device attaches (the 2FA-shaped feature), audit ledger, backup/restore, microVM backend, MCP brokering, multi-user / RBAC, web/PWA reference client.
- **Tier-2 integrations** (separate repos): `dejima-slack`, `dejima-telegram`, ntfy.sh and macOS notification helpers.

## License

Not yet chosen — to be decided before the repo goes public.
