#!/usr/bin/env python3
"""The website's WSL install instruction must match wsl.InstallHint, both ways.

Three binary sites print internal/wsl.InstallHint, and quickstart.html prints
the same commands to the same operator. When those drifted, one person got two
different instructions, which is the defect the constant was created to end.

This checks BOTH DIRECTIONS, and the second one is the point.

  forward  every command in InstallHint appears on the page.
           Catches: the constant gains a step, the page doesn't.

  reverse  the page's install block contains no wsl command that isn't in
           InstallHint.
           Catches: the page gains a step, the constant doesn't.

The first version of this check was forward-only, asserting the page's command
block was a substring of the constant. It passed while the page carried an
extra `wsl --update` step that existed nowhere in the source, because a
page-only addition is exactly what a page-is-a-substring-of-source assertion
cannot see. "The check passes" meant "the parts I copied agree", not "the two
agree" — the blind spot sat on the one half that was drifting.

Run from the repo root: python3 scripts/site-wsl-hint-check.py
"""

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "internal" / "wsl" / "wsl.go"
PAGE = ROOT / "quickstart.html"

# A wsl.exe invocation: the program plus its flags/subcommand, e.g.
# "wsl --install --no-distribution", "wsl --update", "wsl -l -v".
WSL_CMD = re.compile(r"\bwsl(?:\s+-{1,2}[a-z-]+)+")


def install_hint() -> str:
    """Read the constant out of Go source, including its multi-line form."""
    src = SOURCE.read_text(encoding="utf-8")
    m = re.search(r"const InstallHint = (.+?)\n\n", src, re.S)
    if not m:
        sys.exit(f"FAIL: no `const InstallHint` in {SOURCE.relative_to(ROOT)}")
    # Concatenated Go string literals: "a\n" + "b" -> a\nb
    parts = re.findall(r'"((?:[^"\\]|\\.)*)"', m.group(1))
    if not parts:
        sys.exit("FAIL: InstallHint found but no string literal parsed")
    return "".join(parts).replace("\\n", "\n")


def install_block(page: str) -> str:
    """The page region that gives WSL install instructions.

    Scoped by an explicit `data-wsl-install-hint` marker on the page, NOT by
    position. The first version of this walked from the command block to the
    next `</p>`, which was correct for the layout it was written against and
    silently wrong the moment that layout changed — a restructure moved
    `wsl --status` into a disclosure, and a positional region would have swept
    it in and failed on correct copy.

    The scope matters because the Windows panel names commands that are NOT
    install steps: `wsl --status` probes for the feature and `dejima wsl setup`
    builds the distro. Neither belongs in a comparison against a constant that
    is only about installing WSL itself. Marking the region says so in the page,
    where the person editing the page can see it.
    """
    marker = "<div data-wsl-install-hint>"
    start = page.find(marker)
    if start == -1:
        sys.exit(
            "FAIL: quickstart.html has no `data-wsl-install-hint` element.\n"
            "      That marker scopes this check. Without it the check cannot\n"
            "      know which commands it is meant to compare, so it fails\n"
            "      rather than guessing at a region."
        )
    # Walk to the matching </div>; the region contains nested divs.
    depth, i = 0, start
    for m in re.finditer(r"<div\b|</div>", page[start:]):
        depth += 1 if m.group().startswith("<div") else -1
        if depth == 0:
            i = start + m.end()
            break
    else:
        sys.exit("FAIL: `data-wsl-install-hint` element is never closed")
    return re.sub(r"<[^>]+>", " ", page[start:i])


def main() -> int:
    hint = install_hint()
    page = PAGE.read_text(encoding="utf-8")
    block = install_block(page)

    want = {" ".join(c.split()) for c in WSL_CMD.findall(hint)}
    got = {" ".join(c.split()) for c in WSL_CMD.findall(block)}

    print(f"  InstallHint ({SOURCE.relative_to(ROOT)}):")
    for line in hint.split("\n"):
        print(f"    {line.strip()}")
    print(f"  commands in constant: {sorted(want)}")
    print(f"  commands on page:     {sorted(got)}")

    failed = False
    for cmd in sorted(want - got):
        print(f"FAIL: `{cmd}` is in InstallHint but not in the page's install block.")
        print("      The binary tells operators to run it; the website doesn't.")
        failed = True
    for cmd in sorted(got - want):
        print(f"FAIL: `{cmd}` is on the page but not in InstallHint.")
        print("      A page-only step drifts silently. Put it in the constant.")
        failed = True

    if failed:
        return 1
    if not want:
        print("FAIL: parsed zero commands out of InstallHint, so this check")
        print("      cannot see a difference and must not report success.")
        return 1
    print(f"  ok: page and binary agree on all {len(want)} install command(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
