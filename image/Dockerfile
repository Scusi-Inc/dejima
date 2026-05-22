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

# Bundled agents. Both are npm-distributed CLI agents that talk to their
# respective vendor APIs. Other agents (Aider, Goose, etc.) are supported
# via a custom image — see docs/custom-agents.md.
RUN npm install -g @anthropic-ai/claude-code @openai/codex

# Non-root user. Ubuntu 24.04 ships with a default `ubuntu` user at UID 1000;
# delete it so we can keep the canonical UID for our `dejima` user.
RUN userdel -r ubuntu 2>/dev/null || true \
    && useradd -m -s /bin/bash -u 1000 dejima \
    && usermod -aG sudo dejima \
    && echo 'dejima ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers.d/dejima

# tmux defaults: mouse support, large scrollback, no prefix conflicts.
COPY image/tmux.conf /etc/tmux.conf

# Agent shims directory (per-agent, opt-in).
COPY image/agents/ /opt/dejima/agents/

# Entrypoint.
COPY image/start.sh /usr/local/bin/dejima-start.sh
RUN chmod +x /usr/local/bin/dejima-start.sh

USER dejima
WORKDIR /workspace
ENV HOME=/home/dejima \
    GH_CONFIG_DIR=/opt/host/gh-config

# Containers stay alive via dejima-start.sh, which manages tmux + agent process.
ENTRYPOINT ["/usr/local/bin/dejima-start.sh"]
