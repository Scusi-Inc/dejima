# `dejima watch` — see what an agent is doing without standing inside it

**Status:** specified, unbuilt.

## The problem, stated precisely

`dejima connect` attaches your terminal to the agent's tmux session. That session
is the agent's own interactive UI — Claude Code, OpenClaw, aider — and those
programs own the screen: they clear it, redraw it, enter and leave the alternate
screen, and reposition the cursor many times a second.

So **the operator's terminal and the agent's terminal are one surface.** Watching
an agent work is not a passive act; it hands your screen to a program that
assumes it is alone on it.

Three separate incidents came out of that in one week, and all three were
reported as "the terminal went blank" or "a script took over my terminal":

- a test suite prompting for a sudo password on the operator's pane
- `go test` reprinting a failed package's buffered stdout, prompts and all
- a large tool result redrawing the screen mid-read

The first two were real bugs and are fixed. **The third is not a bug.** It is
what attaching means, and no amount of quieting the agent removes it. An agent
that renders anything at all will sometimes render it over what the operator was
reading.

`tmux attach -r` does NOT solve this. Read-only prevents INPUT; it still mirrors
the live screen, so every clear and redraw is still yours.

## What watch is

A **transcript follower**, not a terminal mirror.

    dejima watch <island> [--agent <id>]

It streams what the agent's pane has PRODUCED as append-only text, into the
operator's own terminal, under the operator's own scrollback. No alternate
screen, no clears, no cursor addressing. You can scroll up mid-stream and the
agent cannot take the view back.

The distinction that makes it worth building: `connect` shows you the agent's
CURRENT SCREEN; `watch` shows you WHAT HAPPENED. For "is it still working, and on
what?" — the actual question most of the time — the second is better, and it is
the one that does not cost you your terminal.

## How

`tmux pipe-pane -o` on the agent's pane, appending to a per-agent file on the
island's persisted volume. The daemon tails that file and streams it over the
existing session websocket transport, with ANSI stripped client-side.

Notes that matter for the implementation:

- **Strip at the CLIENT, keep the bytes raw on disk.** A stripped-on-write log
  cannot be re-rendered later with color, and someone will want that. Stripping
  is also where a naive implementation eats legitimate output — an incomplete
  escape sequence at a chunk boundary must be held, not dropped.
- **pipe-pane captures what is WRITTEN, not what is displayed.** A full-screen
  redraw appears as its literal byte stream, so a TUI agent's transcript is
  mostly repaint noise. Collapse repeated frames: keep the last state of a
  redrawn region rather than every intermediate. This is the hard part and the
  reason this is a spec rather than an afternoon.
- **The file grows without bound.** Cap it and rotate, and say so in the stream
  when older content was dropped — a truncated log that reads as complete is the
  failure this codebase keeps finding elsewhere.
- **It must survive a recreate.** The pipe is a property of a tmux pane, so
  `ensureAgentSession` has to re-establish it, the same way it re-establishes the
  session environment. A watch that silently stops following after an upgrade is
  worse than one that never started.

## What it is not

Not a replacement for `connect`. When you need to TYPE at an agent — approve
something, answer a prompt, steer it — you need the real session, and you accept
that it owns the screen while you are there. Watch is for the other 95%.

Not `dejima logs`, which follows the CONTAINER's stdout (the entrypoint, the
daemon-visible process output). An interactive agent's work does not go there;
it goes to a pty inside tmux, which is exactly why this needs pipe-pane.

## Verifying it

    dejima watch notes &
    dejima agent restart notes a1        # the pipe must re-establish
    # scroll up in the watch terminal while the agent renders — the view must hold

The scroll test is the point of the feature. If the agent can pull the view back
to the bottom, it is a mirror with extra steps and has not solved anything.
