#!/usr/bin/env python3
"""Refuse to ship a release whose fit.txt has gone stale.

fit.txt is PUBLIC (dejima.tech/fit.txt, linked from llms.txt and the index) and
its whole value is being candid about what does and does not work. That value is
destroyed by being WRONG, and it has been wrong in both directions inside one
week:

  * "the path works"            — true when written, then the path regressed
  * "has never worked"          — nearly published the day after it started to

Both were written in good faith from the best evidence available, and both would
have been read by someone deciding whether to try Dejima. The failure is not
carelessness, it is that a VERDICT ROTS while a dated observation ages honestly.

So this makes the rot loud. fit.txt records the version it was last revised
against; the release refuses to proceed when that has fallen too far behind.

Two thresholds, deliberately different:

  BEFORE 1.0   a drift budget. Pre-release moves fast and a doc that must be
               rewritten for every patch would just be rewritten carelessly.
  AT 1.0+      ZERO drift. The operator's requirement, and the right one: a
               launch is exactly when someone reads this file for the first
               time, and exactly when "verified at v0.8.67" is indefensible.

Usage:  fit-freshness-check.py <version-being-released>
"""
import re
import sys
from pathlib import Path

DRIFT_BUDGET = 10  # patch releases tolerated before 1.0


def parse(v):
    m = re.match(r"^v?(\d+)\.(\d+)\.(\d+)", v.strip())
    if not m:
        return None
    return tuple(int(x) for x in m.groups())


def main():
    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    releasing = parse(sys.argv[1])
    if releasing is None:
        print(f"error: {sys.argv[1]!r} is not a version", file=sys.stderr)
        return 2

    text = Path("fit.txt").read_text(encoding="utf-8")
    m = re.search(r"last revised[^\n]*?against (v\d+\.\d+\.\d+)", text)
    if not m:
        # Fail rather than skip. A check that cannot find its marker must not
        # report freshness — that is the hollow-guard shape this repo keeps
        # finding, and it would be silently permanent here.
        print("error: fit.txt has no 'last revised … against vX.Y.Z' marker.\n"
              "       This check cannot tell whether it is current, so it will not say it is.",
              file=sys.stderr)
        return 1
    revised = parse(m.group(1))

    print(f"  releasing:        v{'.'.join(map(str, releasing))}")
    print(f"  fit.txt revised:  {m.group(1)}")

    if releasing[0] >= 1:
        if revised != releasing:
            print("\nFAIL: this is a 1.0+ release and fit.txt was last revised against "
                  f"{m.group(1)}.\n"
                  "      fit.txt is public and is the first thing a careful evaluator reads.\n"
                  "      Revise it against this release — confirm what works, and date it —\n"
                  "      then update the 'last revised … against' line.", file=sys.stderr)
            return 1
        print("\nOK: fit.txt is current for a 1.0+ release.")
        return 0

    drift = releasing[2] - revised[2] if releasing[:2] == revised[:2] else DRIFT_BUDGET + 1
    if drift > DRIFT_BUDGET:
        print(f"\nFAIL: fit.txt is {drift} releases behind (budget {DRIFT_BUDGET}).\n"
              "      Re-read it against what actually ships now. Every wrong line in it\n"
              "      this week was a TRUE STATEMENT THAT EXPIRED, so the fix is not more\n"
              "      hedging — it is a fresh look, with dates and versions on any claim\n"
              "      about a path still changing.", file=sys.stderr)
        return 1
    print(f"\nOK: fit.txt is {drift} release(s) behind (budget {DRIFT_BUDGET}).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
