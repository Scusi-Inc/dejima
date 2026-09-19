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

# Each scene is a path through the UI: a start state plus keys to press. Paths
# share frames — frames are deduped by content hash, so a common prefix is
# stored once and the transitions fan out from the shared node.
SCENES = [
    ("browse", "Browse the fleet", ["j", "j", "k", "Space", "j", "Down", "Up"]),
    ("add-agent", "Add an agent", ["Enter", "Down", "Enter", "Escape", "Escape"]),
    ("secrets", "Add a secret", ["s", "Escape"]),
    ("help", "Help", ["?", "Escape"]),
]

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
    def __init__(self, binary, cols=132, rows=40):
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
            now = self.raw()
            if now == last and now.strip():
                stable += 1
                if stable >= 2:
                    return
            else:
                stable = 0
            last = now

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

    frames, order, scenes = {}, [], []

    def record(pane) -> str:
        html = ansi_to_html(pane.raw())
        fid = hashlib.sha256(html.encode()).hexdigest()[:12]
        if fid not in frames:
            frames[fid] = html
            order.append(fid)
        return fid

    for key, title, keys in SCENES:
        with Pane(args.binary) as pane:
            steps = [{"key": None, "frame": record(pane)}]
            for k in keys:
                pane.send(k)
                steps.append({"key": k, "frame": record(pane)})
            scenes.append({"id": key, "title": title, "steps": steps})
            print(f"  {key}: {len(steps)} steps", file=sys.stderr)

    doc = {"cols": 132, "rows": 40, "frames": frames, "scenes": scenes}
    with open(args.out, "w") as fh:
        json.dump(doc, fh, separators=(",", ":"))
    print(f"{len(frames)} unique frames across {len(scenes)} scenes -> {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
