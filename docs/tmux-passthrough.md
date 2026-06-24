# tmux passthrough + extended keys

Dejima runs agents and host terminals inside tmux. tmux sits between the agent's
TUI and the outer terminal, and by default it swallows two classes of escape
sequence that modern agent CLIs (Claude Code, etc.) rely on. Dejima enables both
on the tmux sessions **it creates**, without touching a user's own config.

## What gets enabled

```tmux
set -g allow-passthrough on
set -g extended-keys on
set -as terminal-features ",*:extkeys"
set -ga terminal-overrides ",*:Csu=\E[%p1%d;%p2%du"
```

- **`allow-passthrough on`** — lets DCS / OSC sequences pass straight through
  tmux untouched. This is what makes **terminal image protocols**, **OSC 52
  clipboard**, and **desktop notifications** (OSC 9 / OSC 777) reach the outer
  terminal from inside a tmux pane. Off (the tmux default since 3.3a), tmux
  drops them and the agent's notifications/inline images silently vanish.
- **`extended-keys on` + the `Csu` override** — negotiates the CSI-u "extended
  keys" encoding so tmux can distinguish **Shift+Enter** from a bare Enter (and
  other modified keys) and forward the distinction to the agent. Off, Shift+Enter
  collapses to Enter inside tmux and multi-line input in agent TUIs breaks.

## Where it's applied (and how the user's config is preserved)

There are two kinds of Dejima tmux session, with two delivery paths:

| Session kind | Runs | Config delivery |
|---|---|---|
| In-container **agent** sessions | inside the island container | image's `/etc/tmux.conf` (built from `image/tmux.conf`) |
| **Host terminals** | on the daemon host | `tmux -f ~/.dejima/tmux-host.conf` |

In-container sessions read `/etc/tmux.conf`, which the island image ships — the
container user has no personal `~/.tmux.conf`, so there is nothing to clobber.

Host terminals run on the operator's own host, where they may already keep a
`~/.tmux.conf`. Dejima therefore does **not** edit the user's config. It
materializes its own config to `~/.dejima/tmux-host.conf` (see
`internal/hosttmux`) and passes it with `tmux -f` to **only the host sessions it
creates** (`startHostTmux`, `AttachToHostTmux`). That config's first line is
`source-file -q "~/.tmux.conf"`, so the operator's own customizations still load;
Dejima's passthrough/extended-keys defaults are layered on top. Sessions the
operator starts by hand are unaffected.

The config is re-materialized on every host session create/attach, so a daemon
upgrade picks up changes with no install step.
