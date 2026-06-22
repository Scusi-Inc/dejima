<p align="center">
  <img src="assets/logo.png" alt="Dejima" width="180">
</p>

<h1 align="center">Dejima</h1>

<p align="center"><em>The secure runtime for isolated AI agents — a fleet per project, on your own infrastructure.</em></p>

---

**Dejima** turns a Mac mini or Linux box into a secure, persistent host for AI agents. Built for **coding agent CLIs** (Claude Code, Codex; Aider/Goose/etc. via custom image), and **usable for any agent that wants isolation, persistence, and an API to drive it** — including headless API-SDK loops via `--agent headless --cmd "…"`.

Each project runs in its own *island*: an isolated container with the target repo, the agent of your choice, and a strictly brokered *bridge* back to the host.

> The name and silhouette borrow from the historical island of Dejima in Nagasaki Bay — a single-bridge trading post, for two centuries the only sanctioned point of contact between Japan and the outside world. Same idea here.

## Status

Alpha — in daily use. v1 milestones M0–M5 are implemented and M6 (a week of real-world dogfood on a Mac mini) is met; rough-edge hardening is ongoing. See [`docs/v1-spec.md`](docs/v1-spec.md) and [`docs/v1-milestones.md`](docs/v1-milestones.md).

## Who it's for

Two audiences, same substrate:

- **You run Claude Code / Codex on your home server.** Multiple projects, each in its own container, attachable from any device on your tailnet, surviving disconnects. The painkiller: no more tmux-session sprawl, no more "which window was I in." `dejima init && dejima connect <name>` and you're in.
- **You're building tooling around agents.** The same HTTP/WebSocket API the CLI uses is yours to target — spawn islands, drive sessions, subscribe to lifecycle/agent events, run one-shot commands. Headless agents (any process, not just terminal CLIs) get the same isolation, volumes, credential mounts, and event hooks; you skip the interactive attach surface. Dejima is the substrate; bring your own LangChain / CrewAI / custom loop. See [`docs/agent-adapters.md`](docs/agent-adapters.md).

A note on "API": throughout the docs, *the Dejima API* refers to Dejima's own HTTP/WebSocket control surface. Upstream LLM provider APIs (Anthropic, OpenAI) are reached by the agents *inside* islands, on their own.

## What you get

- **Isolation** — agents run inside containers with no path to your host filesystem, other projects, or unrelated secrets.
- **Multi-project, multi-agent** — N islands on one host, each its own repo; and N agents *within* an island (one container, per-agent git worktrees, shared credentials/tool-auth). No context bleed between islands. See [`docs/multi-agent-spec.md`](docs/multi-agent-spec.md).
- **Persistent sessions** — long-running agent work survives disconnects and host reboots via tmux + named volumes (interactive agents) or supervised processes with captured logs (headless agents).
- **Multi-device attach** — drive the same island from a laptop, phone, or web client. Shared screen, presence-aware.
- **Direct push to GitHub** — the daemon holds one or more GitHub identities (e.g. `work` and `personal`); each island clones and pushes as the one it picks, so `git push` just works from any device. See [`docs/github-identities.md`](docs/github-identities.md).
- **API-first** — the CLI is one client of the Dejima API. Mobile apps, Slack bots, custom integrations target the same surface.
- **Host terminals (opt-in)** — resumable, separately-instanced operator shells on the daemon host itself, for server navigation/repair — the tmux+ssh painkiller. Uncontained and operator-only (`dejimad --host-terminals`); agents stay contained. See [`docs/host-terminals.md`](docs/host-terminals.md).

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

## Install

The shortest path: **get the `dejima` CLI on the machine you'll drive from, run it, follow the wizard.**

```bash
# 1. Get the CLI (Go 1.26+ required; one-shot binary releases are roadmap)
go install github.com/aoos/dejima/cmd/dejima@latest

# 2. Run it. First invocation triggers the setup wizard.
dejima
```

The first-run wizard detects your environment (OS, Docker, Tailscale, dejimad presence), asks what you're trying to do, and prints a tailored set of next steps:

- *Set up the server here* → recommends `make setup` (or runs it for you)
- *Connect to an existing host* → asks for the host, prints `DEJIMA_HOST=…` + how to persist it
- *Both* → server install with a note that the local CLI uses the Unix socket
- *Just exploring* → overview + links

`dejima onboard` re-engages the wizard any time. Say "never" at the first-run prompt to opt out; the wizard stays available via `dejima onboard`.

### Set up the server (alternative: one-liner installer)

If you already know you want the full server stack on this machine, the curl installer skips the wizard and gets you all the way there:

```bash
curl -fsSL https://dejima.tech/install.sh | bash
```

That bootstraps everything: installs Homebrew prereqs (Go, Docker Desktop if needed), clones the source to `~/.dejima-src`, builds the `dejima` + `dejimad` binaries, installs them to `/usr/local/bin`, builds the island image, registers the daemon as a launchd (macOS) or systemd user (Linux) service, and runs `dejima doctor` to verify. Idempotent — re-run to update.

> Served from GitHub Pages at the canonical domain `dejima.tech`. The bare GitHub Pages URL (`aoos.github.io/dejima/install.sh`) and the raw fallback (`raw.githubusercontent.com/aoos/dejima/master/install.sh`) resolve to the same script if DNS is mid-propagation.

### One-liner environment knobs

```bash
# Auto-subscribe a push-notification webhook in the same step
curl -fsSL https://dejima.tech/install.sh \
  | NOTIFY_URL=https://ntfy.sh/your-topic bash

# Non-interactive (auto-install Docker without the y/n prompt)
curl -fsSL https://dejima.tech/install.sh \
  | AUTO_INSTALL_DOCKER=1 bash

# Install binaries to ~/.local/bin instead of /usr/local/bin (no sudo)
curl -fsSL https://dejima.tech/install.sh \
  | PREFIX=$HOME/.local bash
```

### Via Homebrew (macOS, once the tap exists)

```bash
brew install --HEAD aoos/dejima/dejima
```

See [`docs/distribution.md`](docs/distribution.md) for how to create the `aoos/homebrew-dejima` tap. The formula in `homebrew/dejima.rb` is ready to drop in.

> v1 targets macOS (Mac mini priority) and Linux as **hosts** (the machines running `dejimad` + Docker). The `dejima` **client** CLI builds and runs on macOS, Linux, and **Windows** — see *Install (client only)* below.
>
> **macOS**: the installer will offer to install [Docker Desktop](https://www.docker.com/products/docker-desktop/) via Homebrew if Docker isn't present. Docker Desktop is free for personal use and small businesses (<250 employees AND <$10M revenue). Faster alternatives if you prefer to install one yourself first: [OrbStack](https://orbstack.dev) (personal-license only) or [colima](https://github.com/abiosoft/colima) (CLI-only, OSS).
>
> **Linux**: install Docker engine natively from your distro (`docker.io` on Debian/Ubuntu, `docker` on Arch/Fedora) before running the installer. No desktop app needed.

## Install (client only)

To drive a remote Dejima daemon from your laptop or desktop, you only need the CLI — no daemon, no Docker. The CLI builds and runs on **macOS, Linux, and Windows**.

> **Tailscale is the network layer** — the daemon only accepts connections that arrive over a tailnet interface. The installers below detect Tailscale and offer to install + sign you in if it's missing. Both server and client must be on the same tailnet.

### macOS / Linux (one-liner — prebuilt binary)

```bash
curl -fsSL https://dejima.tech/install-client.sh | bash
```

The script:
1. Detects + offers to install Tailscale (Homebrew on macOS, official script on Linux), then walks you through `sudo tailscale up`.
2. Downloads the latest CLI release, verifies SHA256, installs to `/usr/local/bin/dejima`.
3. Prompts for your server's tailnet IP (the one `install.sh` printed on the server), probes it on port 7273, and persists `DEJIMA_HOST` to `~/.zshenv` (macOS) or `~/.bashrc` (Linux).

Skip the prompts with `AUTO_INSTALL_TS=1` and `DEJIMA_HOST_PREFILL=100.x.y.z` env vars.

### macOS / Linux (with Go)

```bash
go install github.com/aoos/dejima/cmd/dejima@latest
dejima                # first-run wizard handles DEJIMA_HOST configuration
```

(Doesn't bundle Tailscale install — you set that up separately.)

### Windows (PowerShell — prebuilt binary)

```powershell
irm https://dejima.tech/install-client.ps1 | iex
```

The script:
1. Detects + offers to install Tailscale via `winget`. Points you at the system-tray icon for sign-in (GUI-only on Windows).
2. Downloads `dejima_<ver>_windows_<arch>.zip` from the latest Release, verifies SHA256, extracts to `%LOCALAPPDATA%\dejima`, adds it to your User PATH.
3. Prompts for the server's tailnet IP, probes :7273 via `Test-NetConnection`, persists `DEJIMA_HOST` to the User environment.

Close + reopen PowerShell to pick up the new PATH and env var, then run `dejima`.

The Windows build has full feature parity with macOS/Linux — TUI, `connect`, all verbs. Terminal-resize is polling-based on Windows (vs. SIGWINCH on Unix) but reflows the same way.

### If you already know the host and want to skip the wizard

```bash
export DEJIMA_HOST=mac-mini.your-tailnet.ts.net:7273
dejima ls
```

Adding `DEJIMA_HOST` to `~/.zshenv` (or `~/.bash_profile`, or your Windows environment) makes it persistent; the installer above does this for you.

## Updating

Re-run the one-liner. The script does a `git pull` on the existing checkout and re-runs `make setup`. New binaries replace the old; existing islands and the daemon's persisted state are untouched.

## Uninstall

> **Get your work out first.** Dejima never touches your host repo folders — but work done
> *inside* an island lives in that island's Docker volume, and purging destroys it. Before
> removing anything, check each island for uncommitted/unpushed work and push it (or copy
> it out):
>
> ```bash
> dejima exec <name> -- git status     # anything uncommitted or unpushed?
> dejima exec <name> -- git push       # send it to the remote
> dejima cp <name>:/workspace/file ./  # or copy files out directly
> ```

```bash
dejima purge <name>                               # each island: container + volumes
dejima service uninstall                          # stop the daemon
sudo rm /usr/local/bin/dejima /usr/local/bin/dejimad
rm -rf ~/.dejima-src                              # source checkout
rm -rf ~/.dejima                                  # persisted state, config, credential seed
# Leftovers, if you skipped purging some islands:
docker ps -aq --filter label=dejima.project | xargs -r docker rm -f
docker volume ls -q --filter label=dejima.project | xargs -r docker volume rm
docker network ls -q --filter name=dejima-net- | xargs -r docker network rm
```

Docker and Tailscale are left in place — Dejima uses them but didn't install-own them. On a
client device, uninstalling is just deleting the `dejima` binary and removing `DEJIMA_HOST`
from your shell profile.

## Install paths under the hood

If you'd rather see exactly what the installer runs, the equivalent manual flow is:

```bash
git clone https://github.com/aoos/dejima.git ~/.dejima-src
cd ~/.dejima-src
make setup        # detect Docker → build → install → image → service → doctor
```

`make setup` itself is just `scripts/setup.sh`; the one-liner is `scripts/install.sh` which adds the prereq-bootstrap + checkout step in front of it. Either is fine.

## Future install paths (roadmap)

- **Homebrew tap** (`brew install aoos/dejima/dejima`) — formula in `homebrew/dejima.rb` is ready; tap repo is on the to-do list.
- **Signed + notarized macOS binaries** — see [`docs/release-notarization.md`](docs/release-notarization.md). Until then, the installers strip the quarantine attribute on download.

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

### Re-engage the setup wizard anytime

```bash
dejima onboard
```

Detects what's already on the machine, asks what you're trying to do (server install / client-only / both / just exploring), and prints a tailored set of commands. Also fires automatically the first time you run `dejima` with no args — say "never" if you don't want it again, "not now" if you want it to ask later.

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
| Island   | The container holding one project and one or more agents.   |
| Agent    | A CLI (Claude Code, Codex) or headless process in an island, on its own git worktree. |
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
dejima upgrade   Recreate container(s) on the current island image; all state kept.
dejima purge     Destroy island and volumes.
dejima exec      Run a one-shot command inside an island.
dejima cp        Copy files in or out of an island.
dejima logs      Tail an island's container logs (--follow).
dejima auth      Manage daemon credentials: Claude login + GitHub identities (push [--github]/status).
dejima doctor    Health check: daemon, Docker, image, projects, networks, webhooks.
dejima image     Build the island image on the daemon host (no source checkout needed).
dejima onboard   Walk through Dejima setup; safe to re-run.
dejima service   Install / uninstall / restart dejimad as a host service (--notify <url>).
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
| Agent can't push to GitHub                            | `dejima auth status` — seed an identity with `dejima auth push --github`, or `gh auth login` on the daemon host. See [`docs/github-identities.md`](docs/github-identities.md).|
| TCP listener: connection refused from off-tailnet     | This is intentional. Only tailnet IPs are accepted on the TCP listener.             |
| Memory creeping over time                             | `dejima hibernate <name>` then `dejima wake <name>` for a clean restart.            |

For more, see [`docs/v1-spec.md`](docs/v1-spec.md).

## Roadmap

See [`docs/roadmap.md`](docs/roadmap.md) for the full prioritized list. Highlights:

- **v1.x hardening** — container watchdog, `panic`, Keychain-backed secrets, idle auto-hibernate. (`upgrade` and credential refresh have shipped.)
- **v2** — trust-on-first-use for new device attaches (the 2FA-shaped feature), audit ledger, backup/restore, microVM backend, MCP brokering, multi-user / RBAC, web/PWA reference client.
- **Tier-2 integrations** (separate repos): `dejima-slack`, `dejima-telegram`, ntfy.sh and macOS notification helpers.

## License

Not yet chosen — to be decided before the repo goes public.
