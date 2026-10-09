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

# Keys worth exploring, from the TUI's own footer and help. `q` is deliberately
# absent: it quits, and a captured post-quit pane is a dead terminal. The page
# handles q itself.
KEYS = [
    "Up", "Down", "Left", "Right", "Space", "Enter", "Escape",
    "j", "k", "n", "s", "C", "?", ">", "m", "E",
]

# How deep to walk. Each extra level multiplies capture time; 3 covers every
# overlay plus a step inside it, which is where the interesting depth is.
MAX_DEPTH = 2

# A hard ceiling on work, so a UI change that makes every keystroke produce a
# distinct frame cannot turn this into an unbounded crawl.
MAX_EDGES = 1200

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
    def __init__(self, binary, cols=170, rows=40):
        self.s = "democap"
        self.binary, self.cols, self.rows = binary, cols, rows

    def __enter__(self):
        subprocess.run(["tmux", "kill-session", "-t", self.s], capture_output=True)
        subprocess.run(["tmux", "new-session", "-d", "-s", self.s,
                        "-x", str(self.cols), "-y", str(self.rows),
                        f"{shlex.quote(self.binary)} tui --demo"], check=True)
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
    ap.add_argument("--max-depth", type=int, default=MAX_DEPTH)
    args = ap.parse_args()

    frames = {}          # fid -> html
    edges = {}           # fid -> {key: fid}
    paths = {}           # fid -> key path from root (how to reach it again)

    def record(pane) -> str:
        raw = pane.raw()
        html = ansi_to_html(raw)
        fid = hashlib.sha256(stable_key(raw).encode()).hexdigest()[:12]
        frames.setdefault(fid, html)
        return fid

    # Root.
    with Pane(args.binary) as pane:
        root = record(pane)
    paths[root] = []
    frontier, seen, explored = [root], {root}, 0

    for depth in range(args.max_depth):
        nxt = []
        for fid in frontier:
            path = paths[fid]
            with Pane(args.binary) as pane:
                for k in path:
                    pane.send(k)
                # Confirm the replay landed where we think it did. A TUI whose
                # state depends on timing would otherwise attribute an edge to
                # the wrong parent, and the demo would teleport.
                if record(pane) != fid:
                    print(f"  ! replay diverged at depth {depth}; skipping node", file=sys.stderr)
                    continue
                for k in KEYS:
                    if explored >= MAX_EDGES:
                        break
                    pane.send(k)
                    dest = record(pane)
                    explored += 1
                    if dest != fid:
                        edges.setdefault(fid, {})[k] = dest
                        if dest not in seen:
                            seen.add(dest)
                            paths[dest] = path + [k]
                            nxt.append(dest)
                    # Try to get back to the parent cheaply. Escape reverses most
                    # overlays; verify by hash rather than trusting it, and replay
                    # from scratch when it did not work.
                    if dest != fid:
                        pane.send("Escape")
                        if record(pane) != fid:
                            pane.restart()
                            for k2 in path:
                                pane.send(k2)
                            if record(pane) != fid:
                                break  # cannot re-establish; move to the next node
            print(f"  depth {depth}: {fid} -> {len(edges.get(fid, {}))} edges "
                  f"({len(frames)} frames, {explored} probes)", file=sys.stderr)
        frontier = nxt
        if not frontier or explored >= MAX_EDGES:
            break

    # Drop anything the root cannot reach. BFS records a frame the moment it is
    # seen, including ones only ever observed while verifying a replay, and an
    # unreachable frame is pure payload — every one is ~10 KB the visitor
    # downloads and can never see.
    reachable, stack = {root}, [root]
    while stack:
        for dest in edges.get(stack.pop(), {}).values():
            if dest not in reachable:
                reachable.add(dest)
                stack.append(dest)
    dropped = len(frames) - len(reachable)
    frames = {k: v for k, v in frames.items() if k in reachable}
    edges = {k: v for k, v in edges.items() if k in reachable}
    if dropped:
        print(f"  pruned {dropped} unreachable frame(s)", file=sys.stderr)

    doc = {"cols": 170, "rows": 40, "root": root, "frames": frames, "edges": edges}
    with open(args.out, "w") as fh:
        json.dump(doc, fh, separators=(",", ":"))
    print(f"{len(frames)} frames, {sum(len(v) for v in edges.values())} edges -> {args.out}",
          file=sys.stderr)


if __name__ == "__main__":
    main()
