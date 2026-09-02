# syntax=docker/dockerfile:1
#
# Dejima island image — the canonical container an agent lives inside.
# Goal: an Ubuntu LTS base with the minimum tools an agent needs, plus
# Claude Code preinstalled as the v1 default agent.

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    TZ=Etc/UTC

# Base tooling. Keep this list intentionally tight; agent-specific extras live
# under image/agents/<agent>/ and are layered in below.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        gnupg \
        jq \
        less \
        openssh-client \
        openssh-sftp-server \
        pipx \
        python3 \
        sudo \
        tmux \
        unzip \
        vim \
    && rm -rf /var/lib/apt/lists/*

# bubblewrap: codex's sandbox. Without it codex warns on every start —
#
#   ⚠ Codex could not find bubblewrap on PATH. … Codex will use the bundled
#     bubblewrap in the meantime
#
# — and falls back to a bundled copy. The fallback works, so this is not a
# failure; it is a warning an operator sees on first contact with a bundled
# agent, about a dependency the IMAGE is supposed to provide. Installing it from
# the distro is also the version the OS patches, which the bundled one is not.
RUN apt-get update && apt-get install -y --no-install-recommends bubblewrap \
    && rm -rf /var/lib/apt/lists/*

# GitHub CLI (gh) for credential helper + `gh pr create`-style affordances.
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
        | gpg --dearmor -o /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && chmod a+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
        > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# Node.js (for npm-installed agent CLIs like Claude Code).
RUN curl -fsSL https://deb.nodesource.com/setup_lts.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# In-island `dejima` CLI: a scoped client so an agent INSIDE an arbitrary island
# can drive its own island over the autonomy path — spawn/reap ephemeral
# sub-agents (`dejima agent add --ephemeral`, `dejima agent rm`), check status,
# read its mailbox — using the DEJIMA_HOST + DEJIMA_TOKEN already injected into
# the container env, with no curl-by-hand. The token is island-scoped, so the
# binary can only ever act on its own island (#61/#65 item 4).
#
# Fetched from the published GitHub release (HTTPS + SHA256-verified), matching
# the gh/node install pattern above. Pinned via --build-arg DEJIMA_VERSION=<tag>
# to match a specific daemon; "latest" (default) tracks the newest release.
# Like the daemon's own version, an island image is not auto-refreshed — rebuild
# the image (`dejima image build`) to update the in-island CLI.
ARG DEJIMA_VERSION=latest
RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "$arch" in amd64|arm64) ;; *) echo "unsupported arch: $arch" >&2; exit 1 ;; esac; \
    ver="${DEJIMA_VERSION}"; \
    if [ "$ver" = "latest" ]; then \
        ver="$(curl -fsSL https://api.github.com/repos/aoos/dejima/releases/latest | jq -r .tag_name)"; \
    fi; \
    if [ -z "$ver" ] || [ "$ver" = "null" ]; then echo "could not resolve dejima release version" >&2; exit 1; fi; \
    tmp="$(mktemp -d)"; \
    base="https://github.com/aoos/dejima/releases/download/${ver}"; \
    asset="dejima_${ver}_linux_${arch}.tar.gz"; \
    curl -fsSL "${base}/${asset}" -o "${tmp}/${asset}"; \
    curl -fsSL "${base}/SHA256SUMS" -o "${tmp}/SHA256SUMS"; \
    want="$(awk -v a="$asset" '$2==a || $2=="*"a || $2=="./"a {print $1}' "${tmp}/SHA256SUMS")"; \
    if [ -z "$want" ]; then echo "no checksum for ${asset} in SHA256SUMS" >&2; exit 1; fi; \
    echo "${want}  ${tmp}/${asset}" | sha256sum -c -; \
    tar -xzf "${tmp}/${asset}" -C "${tmp}"; \
    install -m 0755 "${tmp}/dejima" /usr/local/bin/dejima; \
    rm -rf "${tmp}"; \
    dejima --help >/dev/null

# Non-root user. Ubuntu 24.04 ships with a default `ubuntu` user at UID 1000;
# delete it so we can keep the canonical UID for our `dejima` user.
RUN userdel -r ubuntu 2>/dev/null || true \
    && useradd -m -s /bin/bash -u 1000 dejima \
    && usermod -aG sudo dejima \
    && echo 'dejima ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers.d/dejima

# Pre-create the per-agent state dirs that get backed by named volumes
# (~/.claude, ~/.codex, ~/.agent-state). Docker seeds a fresh volume from the
# image directory it's mounted over, inheriting that directory's ownership — so
# these must exist and be owned by `dejima`. Otherwise the volume mountpoint
# defaults to root:root and the agent shim's mkdir fails, crash-looping the
# container.
RUN mkdir -p /home/dejima/.claude /home/dejima/.codex /home/dejima/.agent-state /workspace \
    && chown -R dejima:dejima /home/dejima /workspace

# tmux defaults: mouse support, large scrollback, no prefix conflicts.
COPY image/tmux.conf /etc/tmux.conf

# Agent shims directory (per-agent, opt-in).
COPY image/agents/ /opt/dejima/agents/

# Island primer: a per-agent "you're in a Dejima island" context file. Each
# adapter shim installs it into that adapter's GLOBAL instructions layer
# (~/.claude/CLAUDE.md, ~/.codex/AGENTS.md) as an idempotent, non-clobbering
# managed block — so every agent understands containment (filesystem model, file
# transfer, the daemon path, mailbox) regardless of the repo. See write-primer.sh.
COPY image/write-primer.sh /opt/dejima/write-primer.sh
COPY image/island-primer.md /opt/dejima/island-primer.md
RUN chmod +x /opt/dejima/write-primer.sh

# Dejima-managed per-island secrets. The daemon bind-mounts a KEY=VALUE file at
# /opt/host/secrets.env; this script PARSES it and emits `export` lines, and the
# profile.d hook evals that output. Sourcing the file directly would make bash
# EXECUTE it, so a token containing a backtick or $(...) would run as a command
# in every new shell — the parser is what keeps values as data.
#
# profile.d (not a per-agent shim) because every interactive agent launches with
# `bash -l`: one hook covers claude-code, codex, shell and anything added later.
COPY image/load-secrets.sh /opt/dejima/load-secrets.sh
RUN chmod +x /opt/dejima/load-secrets.sh && \
    printf '%s\n' \
      '# Dejima: export per-island secrets (see /opt/dejima/load-secrets.sh).' \
      '# Eval OUR quoted output, never the mounted file itself.' \
      'if [ -r /opt/host/secrets.env ]; then' \
      '  eval "$(/opt/dejima/load-secrets.sh)" || true' \
      'fi' \
      > /etc/profile.d/10-dejima-secrets.sh && \
    chmod 0644 /etc/profile.d/10-dejima-secrets.sh

# The npm global prefix lives OUTSIDE /home/dejima so it is NOT captured by the
# per-island home volume. That keeps bundled-agent binaries image-managed
# (refreshed on `dejima upgrade`) while the home volume persists only auth/state
# (~/.npmrc, ~/.claude, ~/.codex, ~/.config, …). Owned by dejima so the agents'
# auto-updaters can still write to it.
RUN mkdir -p /opt/dejima/npm-global && chown -R dejima:dejima /opt/dejima/npm-global
# Writable pipx prefix for tier-2 Python agents (aider) that self-install on
# first launch. Under /opt (not the home volume) so it isn't shadowed per island;
# Ubuntu 24.04's system pip is PEP-668 "externally managed", so pipx — not
# `pip install` — is the sanctioned path for CLI tools.
RUN mkdir -p /opt/dejima/pipx/bin && chown -R dejima:dejima /opt/dejima/pipx

# Entrypoint.
COPY image/start.sh /usr/local/bin/dejima-start.sh
RUN chmod +x /usr/local/bin/dejima-start.sh

USER dejima
WORKDIR /workspace
# NPM_CONFIG_PREFIX gives the dejima user a writable global npm location so
# the bundled CLI agents' auto-updaters (`npm i -g …`) actually succeed
# instead of failing with EACCES against the root-owned /usr/lib/node_modules
# default. It lives under /opt (not /home/dejima) so the per-island home volume
# doesn't shadow image-supplied binaries; the bin dir goes on PATH so `claude` /
# `codex` resolve.
ENV HOME=/home/dejima \
    GH_CONFIG_DIR=/opt/host/gh-config \
    NPM_CONFIG_PREFIX=/opt/dejima/npm-global \
    PIPX_HOME=/opt/dejima/pipx \
    PIPX_BIN_DIR=/opt/dejima/pipx/bin \
    PATH=/opt/dejima/npm-global/bin:/opt/dejima/pipx/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Bundled agents. Both are npm-distributed CLI agents that talk to their
# respective vendor APIs. Installed as the dejima user into NPM_CONFIG_PREFIX
# so on-disk paths are user-owned; auto-updates write to the same prefix.
# Other agents (Aider, Goose, etc.) are supported via a custom image — see
# docs/custom-agents.md.
# claude-code is floored at ^2.1.200 — the release that enables synchronized
# terminal output (DECSET 2026) under tmux 3.4+, fixing the scroll-time render
# corruption its Ink renderer otherwise produces over tmux + Windows Terminal
# (pairs with the `sync` terminal-feature in image/tmux.conf). ^ still tracks
# latest patches; it only guarantees a floor with the fix.
#
# VERIFY THE AGENTS RUN, do not just install them. `npm install -g` SUCCEEDS
# EVEN WHEN A PLATFORM BINARY FAILS TO INSTALL: codex ships its executable as an
# OPTIONAL dependency per platform (@openai/codex-linux-arm64 and friends), and
# npm treats a failed optional dependency as non-fatal by design. The image then
# builds green with an agent that dies on first run:
#
#   Error: Missing optional dependency @openai/codex-linux-arm64.
#   Reinstall Codex: npm install -g @openai/codex@latest
#
# An operator hit that after creating a codex agent — the island came up, the
# agent was "installed", and the binary was not there. Creating the agent cannot
# fix it, because the agent uses this image rather than installing anything.
#
# So assert the end state: if either CLI cannot report its own version, the build
# fails here rather than shipping an island that looks complete.
RUN npm install -g @anthropic-ai/claude-code@^2.1.200 @openai/codex \
 && claude --version \
 && codex --version

# Containers stay alive via dejima-start.sh, which manages tmux + agent process.
ENTRYPOINT ["/usr/local/bin/dejima-start.sh"]
