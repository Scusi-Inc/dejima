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
        sudo \
        tmux \
        unzip \
        vim \
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

# The npm global prefix lives OUTSIDE /home/dejima so it is NOT captured by the
# per-island home volume. That keeps bundled-agent binaries image-managed
# (refreshed on `dejima upgrade`) while the home volume persists only auth/state
# (~/.npmrc, ~/.claude, ~/.codex, ~/.config, …). Owned by dejima so the agents'
# auto-updaters can still write to it.
RUN mkdir -p /opt/dejima/npm-global && chown -R dejima:dejima /opt/dejima/npm-global

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
    PATH=/opt/dejima/npm-global/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Bundled agents. Both are npm-distributed CLI agents that talk to their
# respective vendor APIs. Installed as the dejima user into NPM_CONFIG_PREFIX
# so on-disk paths are user-owned; auto-updates write to the same prefix.
# Other agents (Aider, Goose, etc.) are supported via a custom image — see
# docs/custom-agents.md.
RUN npm install -g @anthropic-ai/claude-code @openai/codex

# Containers stay alive via dejima-start.sh, which manages tmux + agent process.
ENTRYPOINT ["/usr/local/bin/dejima-start.sh"]
