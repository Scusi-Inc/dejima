# Host Provisioning Plan

**Status:** Plan (not yet built)
**Owner:** v1.x

The strategic case for `dejima onboard --provision-host`: Dejima becomes the easy on-ramp to "turn a Mac mini into a personal AI agent server." Not just *"better than tmux for agents I'm already running"* — but *"a fresh Mac mini → secure remote AI server, in 20 minutes, one command."*

## Why this matters

Today's positioning is "OSS substrate for agent execution." That's accurate but it sells *adoption* to someone who already has a configured server. The bigger audience is **people who buy a Mac mini specifically to run agents** and don't know where to start.

The current first-timer journey:

```
buy Mac mini → ??? → working Dejima
```

The `???` is hours of:
- "How do I keep this from sleeping?"
- "Do I need SSH? How do I enable it?"
- "Tailscale, Docker, Homebrew, gh — what order, what versions?"
- "How do I make SSH not lose `tmux` over PATH?"
- "What does 'secure' mean for a box I want reachable from my phone?"

Every one of those is a friction point that loses people. The provisioning wizard collapses that gap.

### One-line value prop after this ships

> *"Get a Mac mini. Run one command. Twenty minutes later you have a secure, isolated, multi-device home AI agent server."*

That's a compelling pitch for the "I want to run agents at home" persona that doesn't really have a clean answer today.

## Persona

**Primary: the home-AI-server first-timer**
- Has heard about Claude Code / Codex / agent CLIs
- Has bought (or is about to buy) a Mac mini for this purpose
- Comfortable with terminal, *not* comfortable with sysadmin
- Has Tailscale (or will install it on instruction)
- Wants something that walks them through, not a 20-step README

**Secondary: the experienced user provisioning a new box**
- Knows what each step does
- Wants `--yes` to skip prompts and just run the automatable parts
- Wants a checklist for the GUI-only steps they can't automate

## User journey (target)

```
$ go install github.com/aoos/dejima/cmd/dejima@latest
$ dejima onboard --provision-host
```

Or, after the GitHub Pages site is live:

```
$ curl -fsSL dejima.tech/provision | bash
```

From there: a series of phases (described below). Each phase shows current state, does what it can, instructs for what it can't, verifies, moves on. Resumable; if the user exits mid-flow, re-running picks up where they left off.

## Phases

Each phase has three operations: **detect → act → verify**. Detection skips already-done steps. Act either runs the command or prompts the user to do it (GUI-only steps). Verify confirms the act succeeded before moving on.

### Phase 1 — System config (GUI-instructed)

These require System Settings interaction. The wizard shows the current state, points to the exact menu path, pauses, then verifies.

| Setting | Detection | Where in System Settings |
|---|---|---|
| Sleep prevention | `pmset -g \| grep sleep` | Energy Saver → "Prevent automatic sleeping when display is off" |
| Wake on network | `pmset -g \| grep womp` | Energy Saver → "Wake for network access" |
| Auto-restart after power failure | `pmset -g \| grep autorestart` | Energy Saver → "Start up automatically after a power failure" |
| Remote Login (SSH) | `sudo systemsetup -getremotelogin` | General → Sharing → Remote Login |
| Screen Sharing | `defaults read /Library/Preferences/com.apple.RemoteManagement.plist` | General → Sharing → Screen Sharing |
| Computer name | `scutil --get ComputerName` | General → About → Name (or `sudo scutil --set ComputerName <name>` direct) |

Effort: most of this is *instructional* — the wizard prints the menu path and waits. A few (`pmset`, `scutil`) we can run directly with sudo.

### Phase 2 — Tooling install (mostly automatable)

| Tool | Install | Already-installed check |
|---|---|---|
| Homebrew | `bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"` | `command -v brew` |
| Tailscale | `brew install --cask tailscale` → prompt to launch + sign in | `tailscale status` returns connected |
| Docker (Desktop) | `brew install --cask docker` → prompt to launch + grant permissions | `docker version` reaches a server |
| GitHub CLI (`gh`) | `brew install gh` then `gh auth login` | `gh auth status` returns logged in |

Effort: shell out to brew. The Tailscale + Docker first-launches are the only GUI pauses.

### Phase 3 — Shell / SSH config (automatable)

| Config | What it does | Detection |
|---|---|---|
| `~/.zshenv` PATH | Adds `/opt/homebrew/bin` so non-interactive SSH inherits brew binaries | grep for the line |
| `~/.ssh/authorized_keys` | Prompts for laptop's public key; appends if not present | grep for the key |
| SSH password disable | `sudo sed -i '' 's/^#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config` (with confirm) | grep `sshd_config` |
| `~/.gitconfig` user.name/email | Prompts for values if unset | `git config --get user.name` |

Effort: straightforward file edits. The password-disable step is the most security-consequential and should be explicit + opt-in (not auto).

### Phase 4 — Dejima install (existing logic)

Reuses the existing `make setup` / `dejima onboard` flow. The provision wizard hands off here.

- Build + install Dejima binaries
- Build the island image (`make image`)
- `dejima service install` for the daemon
- Notification webhook config (`--notify <url>`)

### Phase 5 — Verify + handoff

- `dejima doctor` — full health check
- Print the connection info for client devices:
  ```
  ✅ Setup complete.

  This host's Tailscale FQDN: minion.tail2f808e.ts.net

  From your laptop:
    go install github.com/aoos/dejima/cmd/dejima@latest
    export DEJIMA_HOST=minion.tail2f808e.ts.net:7273
    dejima ls
  ```
- Optionally: write a one-page printable cheatsheet to `~/Desktop/dejima-quick-reference.txt`.

## State management

The wizard writes its progress to `~/.dejima/provisioning-state.json`:

```json
{
  "started_at": "2026-05-22T08:00:00Z",
  "completed_phases": ["system-config", "tooling-install"],
  "current_phase": "shell-config",
  "skipped": ["screen-sharing"],
  "answers": { "ssh_password_disable": false }
}
```

Re-running `dejima onboard --provision-host` reads this and resumes. Useful when the user exits mid-flow (e.g., waiting for Docker Desktop to finish installing).

Once all phases finish, the state file is left in place as a record. `--reset` clears it.

## Implementation breakdown

| Chunk | Effort |
|---|---|
| Phase abstraction (struct with Check/Run/Verify, state machine to walk through) | ~1 day |
| Phase 1 — System config: GUI instructions + verifiers via `pmset`/`systemsetup`/`scutil` | ~1 day |
| Phase 2 — Brew + Tailscale + Docker + gh install logic | ~1 day |
| Phase 3 — SSH + shell config edits, key prompt, password-disable opt-in | ~0.5 day |
| Phase 4 — Hand-off to existing wizard | ~0.5 day (mostly already exists) |
| Phase 5 — Final verify + handoff message + cheatsheet | ~0.5 day |
| State persistence + resume logic | ~0.5 day |
| End-to-end testing on a clean Mac mini install (or VM) | ~1 day |
| Docs (companion runbook with screenshots, README updates) | ~0.5 day |

**Total: ~5-6 days of focused work** for a polished v1.

## Non-goals

To bound scope:

- **Not a general-purpose Mac configuration tool.** No dotfiles management, no themes, no editor setup. The line is: "what does a Mac mini need to be a Dejima server?"
- **Not a security hardening framework.** It'll *offer* a few hardening steps (SSH password disable, key-only auth) but won't try to be `lynis` or CIS-compliant. Tailscale + container isolation + opt-in hardening is the security model.
- **Not Linux-aware.** macOS-only. A Linux equivalent is its own project (~similar effort, different commands, different package managers). Document separately.
- **Not idempotent in the strict sense.** "Skip if already done" yes; "perfectly re-runnable in any order" no — too brittle. State machine is enough.
- **Not a replacement for the existing wizard.** Provisioning is the *outer* shell that ends by handing off to the existing flow.

## Risks

| Risk | Mitigation |
|---|---|
| macOS version drift breaks the System Settings instructions | Embed a check: `sw_vers -productVersion` → tailor menu paths. Document tested versions. |
| GUI pauses confuse non-interactive users | `--yes` flag skips them, emits a checklist of "you still need to do these manually" at the end |
| User exits mid-flow and Dejima ends up half-configured | State file + clean error messages on resume |
| Security implications of an internet-accessible Mac mini | Tailscale-only is the default and the recommendation; document the threat model explicitly |
| Scope creep into "while you're at it, configure X" | Hard list of phases; new phases require a separate decision, not casual additions |

## Strategic implications when this ships

### README + landing page reframe

Headline becomes some version of:

> **The easy way to turn a Mac mini into a personal AI agent server.**
> *Open-source, secure, multi-device. One command, twenty minutes, done.*

The "fresh Mac mini" persona becomes the primary install path on the landing page:

```bash
# On a fresh Mac mini:
curl -fsSL dejima.tech/provision | bash
```

The existing "advanced / I know what I want" install paths move below as alternatives.

### Distribution becomes the marketing moment

A single screenshot or 90-second video of *"Mac mini boot → run command → 20-minute walkthrough → working remote AI server"* is the most compelling demo this project could ship. Way more visceral than "API substrate for agents."

### What this doesn't change

- Dejima is still substrate, not product. The provisioning wizard configures a *host*; it doesn't replace Scusi / Dejima-Slack / other consumers.
- The CLI / API surface stays unchanged.
- Existing users on already-configured hosts use `dejima onboard` (no `--provision-host`) and skip all this.

## What to do next

1. **Land the existing TUI + wizard work on real dogfood** (in progress).
2. **Once we have one full clean-Mac-mini install behind us**, the friction points from that run inform which provisioning phases are highest-value.
3. **Build the provisioning wizard** as a v1.x effort, ~1 week.
4. **Reframe the landing page** to lead with "fresh Mac mini" persona once the wizard works.
5. **Capture a short demo video** to anchor the marketing pitch.
