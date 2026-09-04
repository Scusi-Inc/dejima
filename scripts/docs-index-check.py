#!/usr/bin/env python3
"""docs/README.md must stay true: every doc listed once, every link resolving.

An index is a reading of the tree, and a reading goes stale exactly the way
docs/testing/readings-go-stale.md describes — silently, while still looking
authoritative. An index that omits a third of the docs is worse than none,
because a reader who finds no index knows to go looking and a reader who finds
an incomplete one does not.

Two failures, and they are asymmetric on purpose:

  BROKEN LINK      unambiguous. The index points at something that is not there.
  UNLISTED DOC     a new file nobody indexed. One line in README.md fixes it, or
                   one line in the waiver list below with a reason.

The waiver list is the same ratchet the coverage gate uses: skipping is allowed
and it costs a deliberate, reviewed edit rather than silence.

Run: python3 scripts/docs-index-check.py
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(ROOT, "docs")
INDEX = os.path.join(DOCS, "README.md")

# Docs deliberately not in the index, each with the reason. Adding a line here
# is a choice someone reviews, which is the entire point.
WAIVED = {
    "README.md": "is the index",
}

LINK = re.compile(r"\]\(([^)#]+?)(?:#[^)]*)?\)")


def main() -> int:
    if not os.path.isfile(INDEX):
        print("error: docs/README.md is missing — there is no index to check.", file=sys.stderr)
        return 1
    with open(INDEX, encoding="utf-8") as fh:
        text = fh.read()

    # Every doc on disk, relative to docs/.
    on_disk = set()
    for dirpath, _dirs, files in os.walk(DOCS):
        for name in files:
            if name.endswith(".md"):
                rel = os.path.relpath(os.path.join(dirpath, name), DOCS)
                on_disk.add(rel.replace(os.sep, "/"))

    # Every link the index makes, and whether it resolves.
    linked, broken = set(), []
    for target in LINK.findall(text):
        if target.startswith(("http://", "https://", "mailto:")):
            continue
        norm = os.path.normpath(target).replace(os.sep, "/")
        linked.add(norm)
        if not os.path.exists(os.path.join(DOCS, norm)):
            broken.append(target)

    missing = sorted(on_disk - linked - set(WAIVED))
    failed = False

    if broken:
        failed = True
        print("FAIL: docs/README.md links to files that do not exist:", file=sys.stderr)
        for b in sorted(set(broken)):
            print(f"  {b}", file=sys.stderr)
        print("       Fix the link or remove the entry. A pointer at nothing is\n"
              "       worse than no pointer: it reads as a promise.", file=sys.stderr)

    if missing:
        failed = True
        print(f"\nFAIL: {len(missing)} doc(s) exist but are not in docs/README.md:", file=sys.stderr)
        for m in missing:
            print(f"  docs/{m}", file=sys.stderr)
        print("       Add each under the question it answers, or waive it in\n"
              "       scripts/docs-index-check.py with the reason. An index that\n"
              "       silently omits files is the failure it exists to prevent.", file=sys.stderr)

    if failed:
        return 1
    print(f"OK: docs/README.md indexes all {len(on_disk) - len(WAIVED)} docs; every link resolves.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
