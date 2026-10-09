#!/usr/bin/env python3
"""Capture the REAL Dejima TUI as frames for the site's interactive demo.

WHY THIS EXISTS RATHER THAN A HAND-BUILT MOCK. herdr's landing page has a
hand-built terminal mock, and their own page admits what happens next: "the mock
was on v2.1.168, Claude Code is v2.1.198 now". A mock is a second implementation
of the UI, and it starts drifting the day it ships.

These frames ARE the TUI. `dejima tui --demo` drives a synthetic fleet with no
daemon, no network and no real repos or secrets (see cmd/dejima/tui_demo.go), so
the frames are reproducible and leak nothing. Re-run this after a TUI change and
the demo is current again; the diff is the review.

Usage:  scripts/capture-demo-frames.py <dejima-binary> [-o demo-frames.json]
"""
import argparse
import hashlib
import json
import re
import shlex
import subprocess
import sys
import time

# THE DEMO IS A GRAPH, NOT A TOUR.
#
# This used to be six hardcoded key paths (SCENES). That produced a demo where
# only the keys on a rehearsed path did anything: a visitor pressed Escape, or
# an arrow the script had not walked, and got nothing. On a landing page a dead
# keystroke reads as a broken product, and being railroaded reads as a product
# with less in it than it has.
#
# So: breadth-first from the root, trying EVERY key the TUI answers from EVERY
# state we reach, and recording the transition. The result is an edge map the
# page can walk freely — the visitor drives, and every key that does something
# in the real TUI does the same thing here, because every edge in the map was
# produced by pressing that key on the real TUI.
#
# Frames dedupe by content hash, so states reached by different routes collapse
# to one node and the graph stays small.

# MOVE keys walk the list. They are captured by WALKING, not by probing: press
# Down to the bottom recording each step, then k back to the top, then j down,
# then Up back — four passes, every edge a real keypress, and the whole list
# navigable in both directions with both key names.
#
# This replaced a plain breadth-first crawl, which spent its probe budget on
# breadth and reached depth two. Arrow navigation is the main thing anyone does
# with a list, and it dead-ended after a single press: "nothing bound to that
# here". Breadth was the wrong axis.
MOVE_PASSES = [("Down", "k"), ("j", "Up")]

# OPEN keys are probed at every row on the way down, because they are reversible:
# press, record, press Escape, confirm by hash that we are back where we were.
# That is what makes probing them at every node affordable.
OPEN_KEYS = ["Space", "Enter", "s", "?", "n", "C", ">", "m", "E"]

# How far down the list to walk before giving up on finding the end. The walk
# stops early when a press changes nothing, which is what the bottom looks like.
MAX_WALK = 24

# The captured terminal geometry. One place, so the JSON, the harness and the
# page cannot disagree about how wide a frame is.
COLS, ROWS = 136, 40

# THE FLEET ANIMATES, AND THAT IS NOT A STATE CHANGE.
#
# `tui --demo` ticks memory, CPU and uptime, so two captures of the SAME screen
# differ. Hashing raw output therefore minted a new "state" on every tick, and
# the cheap return-to-parent check (press Escape, compare hashes) never matched
# -- so the harness restarted the pane for nearly every probe and ran about
# thirty times slower than the keystrokes themselves cost.
#
# Identity is the LAYOUT, not the numbers: navigation states differ by what is
# selected and which overlay is up, never by a digit. So settle and hash on text
# with the volatile runs flattened, and store the real capture for display.
VOLATILE = re.compile(r"[0-9]+(?:\.[0-9]+)?")


# The page cannot see what the TUI has selected: a frame is opaque HTML, so
# "Enter opens the highlighted thing" is unanswerable from the frame alone. The
# capture knows, though — the cursor is on screen — so it reads the highlighted
# row out of each frame and ships it as metadata. That is what lets Enter mean
# the same thing here as in the real TUI: all of an island's agents, or one
# agent.
CURSOR = "\u25b6"  # ▶ , the row marker


def selected_row(text: str):
    """Return {kind,label} for the highlighted row, or None."""
    for line in text.split("\n"):
        if CURSOR not in line:
            continue
        body = line.split(CURSOR, 1)[1]
        body = body.replace("\u2502", " ")  # │ panel borders
        agent = any(g in body for g in ("\u251c", "\u2514"))  # ├ └ : a child row
        for g in "\u25be\u25b8\u25cf\u25c6\u25a0\u251c\u2514\u2500\u21d5\u26b7!":
            body = body.replace(g, " ")
        label = " ".join(body.split())
        if not label:
            return None
        # Island rows carry an agent count: "nimbus-api (4)  running · …".
        m = re.match(r"^([A-Za-z0-9._-]+)\s*\((\d+)\)", label)
        if m and not agent:
            return {"kind": "island", "label": m.group(1), "agents": int(m.group(2))}
        name = label.split("  ")[0].strip()
        if name.startswith("+") or name.startswith("secrets"):
            return {"kind": "other", "label": name}
        return {"kind": "agent" if agent else "other", "label": name}
    return None


def stable_key(text: str) -> str:
    return VOLATILE.sub("#", text)


SGR = re.compile(r"\x1b\[([0-9;]*)m")
OSC = re.compile(r"\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)")
CSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]")

# xterm's 16 base colours, as the site renders them. Kept here rather than in CSS
# so a frame is self-contained HTML and the page needs no palette knowledge.
BASE = ["#1e2228", "#e06c75", "#98c379", "#e5c07b", "#61afef", "#c678dd", "#56b6c2", "#c9d1d9",
        "#5c6370", "#ff7b72", "#b5e08a", "#f0d399", "#79c0ff", "#d2a8ff", "#76e0e8", "#f0f6fc"]


def xterm256(n: int) -> str:
    if n < 16:
        return BASE[n]
    if n < 232:
        n -= 16
        r, g, b = (n // 36) % 6, (n // 6) % 6, n % 6
        f = lambda v: 0 if v == 0 else 55 + 40 * v
        return "#%02x%02x%02x" % (f(r), f(g), f(b))
    v = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (v, v, v)


class Style:
    """The SGR state machine. Only what the TUI actually emits."""

    def __init__(self):
        self.reset()

    def reset(self):
        self.fg = self.bg = None
        self.bold = self.dim = self.italic = self.underline = self.reverse = False

    def apply(self, params):
        # A bare ESC[m is ESC[0m. Splitting "" would otherwise yield no codes at
        # all and silently leave the previous style running for the rest of the
        # line — which looks like a rendering bug in the TUI rather than here.
        codes = [int(p) for p in params.split(";") if p != ""] or [0]
        i = 0
        while i < len(codes):
            c = codes[i]
            if c == 0:
                self.reset()
            elif c == 1:
                self.bold = True
            elif c == 2:
                self.dim = True
            elif c == 3:
                self.italic = True
            elif c == 4:
                self.underline = True
            elif c == 7:
                self.reverse = True
            elif c in (22, 23, 24, 27):
                self.bold = self.dim = False if c == 22 else self.bold
                if c == 23:
                    self.italic = False
                if c == 24:
                    self.underline = False
                if c == 27:
                    self.reverse = False
            elif 30 <= c <= 37:
                self.fg = BASE[c - 30]
            elif 90 <= c <= 97:
                self.fg = BASE[c - 90 + 8]
            elif 40 <= c <= 47:
                self.bg = BASE[c - 40]
            elif 100 <= c <= 107:
                self.bg = BASE[c - 100 + 8]
            elif c == 39:
                self.fg = None
            elif c == 49:
                self.bg = None
            elif c in (38, 48):
                target = "fg" if c == 38 else "bg"
                if i + 1 < len(codes) and codes[i + 1] == 5:
                    setattr(self, target, xterm256(codes[i + 2])); i += 2
                elif i + 1 < len(codes) and codes[i + 1] == 2:
                    setattr(self, target, "#%02x%02x%02x" % tuple(codes[i + 2:i + 5])); i += 4
            i += 1

    def css(self):
        fg, bg = self.fg, self.bg
        if self.reverse:
            fg, bg = bg or BASE[0], fg or BASE[7]
        out = []
        if fg:
            out.append("color:" + fg)
        if bg:
            out.append("background:" + bg)
        if self.bold:
            out.append("font-weight:600")
        if self.dim:
            out.append("opacity:.6")
        if self.italic:
            out.append("font-style:italic")
        if self.underline:
            out.append("text-decoration:underline")
        return ";".join(out)


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def ansi_to_html(text: str) -> str:
    """Convert one captured pane into HTML spans.

    Done HERE rather than in the browser on purpose: the site is static with no
    build step, so shipping pre-rendered spans costs no runtime dependency and
    the first frame renders with JS disabled.
    """
    text = OSC.sub("", text)
    out, st = [], Style()
    for line in text.split("\n"):
        line = CSI.sub(lambda m: m.group(0) if m.group(0).endswith("m") else "", line)
        pos, buf = 0, []
        for m in SGR.finditer(line):
            chunk = line[pos:m.start()]
            if chunk:
                css = st.css()
                buf.append(f'<span style="{css}">{esc(chunk)}</span>' if css else esc(chunk))
            st.apply(m.group(1))
            pos = m.end()
        tail = line[pos:]
        if tail:
            css = st.css()
            buf.append(f'<span style="{css}">{esc(tail)}</span>' if css else esc(tail))
        out.append("".join(buf))
    return "\n".join(out)


class Pane:
    def __init__(self, binary, cols=COLS, rows=ROWS):
        self.s = "democap"
        self.binary, self.cols, self.rows = binary, cols, rows

    def __enter__(self):
        subprocess.run(["tmux", "kill-session", "-t", self.s], capture_output=True)
        # DEJIMA_DEMO_FREEZE stops the fleet animating. Without it the agent
        # state words churn on a timer, every visit to the same screen hashes
        # differently, and the graph fragments into hundreds of one-shot frames
        # (see demoFrozen in cmd/dejima/tui_demo.go).
        subprocess.run(["tmux", "new-session", "-d", "-s", self.s,
                        "-x", str(self.cols), "-y", str(self.rows),
                        f"DEJIMA_DEMO_FREEZE=1 {shlex.quote(self.binary)} tui --demo"], check=True)
        # THE SIZE HAS TO BE DELIVERED, not merely declared, and this is the whole
        # reason the first version of this harness produced garbage.
        #
        # `new-session -x/-y` sets the session's size, but a DETACHED session has
        # no client, so no SIGWINCH is sent and bubbletea never receives a
        # WindowSizeMsg. It falls back and renders at 93 columns forever — at a
        # declared 100, 120 and 160 alike. The panes come out too narrow and rows
        # clip: `working` as `wo`, `needs you` as `ne`, `api-gateway` as
        # `api-gatew…`.
        #
        # That read as a TUI bug, survived the obvious control (widen the
        # terminal — which changed nothing, because nothing reached the program),
        # and was reported as one. It is not: rendered directly in Go at a known
        # width the same row fits. resize-window forces the event the detached
        # session never sends.
        #
        # See docs/testing/readings-go-stale.md, "The instrument was stale, not
        # the reading" — three declared widths produced byte-identical output,
        # and a measurement that does not move when its input moves is not
        # measuring its input.
        time.sleep(1.5)
        subprocess.run(["tmux", "resize-window", "-t", self.s,
                        "-x", str(self.cols), "-y", str(self.rows)], capture_output=True)
        self._settle()
        return self

    def __exit__(self, *a):
        subprocess.run(["tmux", "kill-session", "-t", self.s], capture_output=True)

    def _settle(self):
        # The fleet animates on a tick, so a frame taken mid-churn differs from
        # the next one for reasons that have nothing to do with the keystroke.
        # Wait for two identical captures before recording.
        last, stable = None, 0
        for _ in range(60):
            time.sleep(0.4)
            now = stable_key(self.raw())
            if now == last and now.strip():
                stable += 1
                if stable >= 2:
                    return
            else:
                stable = 0
            last = now

    def restart(self):
        subprocess.run(["tmux", "kill-session", "-t", self.s], capture_output=True)
        self.__enter__()

    def raw(self) -> str:
        return subprocess.run(["tmux", "capture-pane", "-t", self.s, "-p", "-e"],
                              capture_output=True, text=True).stdout

    def send(self, key: str):
        subprocess.run(["tmux", "send-keys", "-t", self.s, key], check=True)
        self._settle()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("binary")
    ap.add_argument("-o", "--out", default="demo-frames.json")
    args = ap.parse_args()

    frames, edges, rows = {}, {}, {}

    def record(pane) -> str:
        raw = pane.raw()
        html = ansi_to_html(raw)
        fid = hashlib.sha256(stable_key(raw).encode()).hexdigest()[:12]
        if fid not in frames:
            frames[fid] = html
            sel = selected_row(CSI.sub("", OSC.sub("", SGR.sub("", raw))))
            if sel:
                rows[fid] = sel
        return fid

    def link(a, key, b):
        if a != b:
            edges.setdefault(a, {})[key] = b

    with Pane(args.binary) as pane:
        root = record(pane)

        # --- pass 1: walk down, probing the reversible keys at every row -----
        spine = [root]
        cur = root
        for _ in range(MAX_WALK):
            for k in OPEN_KEYS:
                pane.send(k)
                dest = record(pane)
                if dest == cur:
                    continue  # key does nothing here; nothing to record
                link(cur, k, dest)
                pane.send("Escape")
                if record(pane) != cur:
                    # Escape did not bring us back. Rebuild the position from
                    # the root rather than guess, so later edges are not
                    # attributed to the wrong row.
                    pane.restart()
                    back = record(pane)
                    for step in spine[1:]:
                        pane.send("Down")
                        back = record(pane)
                    if back != cur:
                        print("  ! lost position; stopping the walk", file=sys.stderr)
                        break
            pane.send("Down")
            nxt = record(pane)
            if nxt == cur:
                break  # bottom of the list
            link(cur, "Down", nxt)
            spine.append(nxt)
            cur = nxt
        print(f"  walked {len(spine)} rows, {len(frames)} frames", file=sys.stderr)

        # --- remaining passes: the other three move keys, in both directions --
        for down_key, up_key in MOVE_PASSES:
            # Up to the top.
            for i in range(len(spine) - 1, 0, -1):
                pane.send(up_key)
                dest = record(pane)
                link(spine[i], up_key, dest)
            # Down to the bottom.
            for i in range(0, len(spine) - 1):
                pane.send(down_key)
                dest = record(pane)
                link(spine[i], down_key, dest)
            print(f"  pass {down_key}/{up_key}: {sum(len(v) for v in edges.values())} edges",
                  file=sys.stderr)

    # Drop anything the root cannot reach. A frame recorded while re-establishing
    # position is pure payload -- every one is ~10 KB the visitor downloads and
    # can never see.
    reachable, stack = {root}, [root]
    while stack:
        for dest in edges.get(stack.pop(), {}).values():
            if dest not in reachable:
                reachable.add(dest)
                stack.append(dest)
    dropped = len(frames) - len(reachable)
    frames = {k: v for k, v in frames.items() if k in reachable}
    edges = {k: v for k, v in edges.items() if k in reachable}
    rows = {k: v for k, v in rows.items() if k in reachable}
    if dropped:
        print(f"  pruned {dropped} unreachable frame(s)", file=sys.stderr)

    doc = {"cols": COLS, "rows": ROWS, "root": root, "frames": frames,
           "edges": edges, "selected": rows}
    with open(args.out, "w") as fh:
        json.dump(doc, fh, separators=(",", ":"))
    print(f"{len(frames)} frames, {sum(len(v) for v in edges.values())} edges -> {args.out}",
          file=sys.stderr)


if __name__ == "__main__":
    main()
