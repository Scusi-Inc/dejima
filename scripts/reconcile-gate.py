#!/usr/bin/env python3
"""Every path that brings a container UP must reconcile the non-primary agents.

The island entrypoint relaunches the PRIMARY agent only. Every co-located agent
is the daemon's job, so a handler that creates or starts a container and returns
without calling reconcileAgentsAsync leaves those agents with no tmux session at
all — until some unrelated path reconciles them by accident.

This has now been found four times, in four handlers, by three people:

  a0bd706  upgradeIsland      brought agent 0 back alone
  14a7ac1  AdoptExisting, restartToRunning, startIslandIfStopped
  #445     resetIsland
  wake.go  wakeIslandFor      wake-on-message

WHY A GATE AND NOT A FIFTH COMMENT. 14a7ac1 and #445 were both found by grepping
for CALLERS OF reconcileAgents and checking the value each passed. That search
enumerates "paths that reconcile badly". The real set is "paths that bring a
container up" — and a handler with the bug in its most complete form, no
reconcile whatsoever, has no row in the first table. Twice now the milder form
masked the worse one. The repo's own rule: a lesson that recurs twice becomes a
check.

So this asks the question from the other end. It enumerates every function that
calls createContainerForProject or rt.StartContainer, and requires each to
reconcile.

WHAT IT DOES NOT CHECK, deliberately: the resume VALUE. That invariant is "ask
when you did not bake; keep the literal when you did" (14a7ac1), it is
genuinely per-call-site, and a checker that guessed at it would be wrong in both
directions. This gate answers one question — does this path reconcile at all —
and answers it mechanically.

Run: python3 scripts/reconcile-gate.py
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PKG = os.path.join(ROOT, "internal", "api")

# Bringing a container up. createContainerForProject ends at `docker run -d`
# (internal/runtime/docker.go), so it starts what it creates.
BRINGS_UP = re.compile(r"\bcreateContainerForProject\(|\brt\.StartContainer\(")
RECONCILES = re.compile(r"\breconcileAgents(?:Async)?\(")
FUNC = re.compile(r"^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)")

# Functions that bring a container up and correctly do NOT reconcile, each with
# the reason. Adding a line here is a deliberate, reviewed edit — which is the
# entire point of it being a list rather than a heuristic.
WAIVED = {
    # The definition itself: it creates the container its callers then reconcile.
    "createContainerForProject": "is the primitive; every caller reconciles",
}


def functions(path):
    """Yield (name, start_line, body) for each top-level func in a gofmt'd file."""
    with open(path, encoding="utf-8") as fh:
        lines = fh.readlines()
    cur, start, buf = None, 0, []
    for i, line in enumerate(lines, 1):
        m = FUNC.match(line)
        if m:
            if cur:
                yield cur, start, "".join(buf)
            cur, start, buf = m.group(1), i, [line]
        elif cur:
            buf.append(line)
            if line.startswith("}"):
                yield cur, start, "".join(buf)
                cur, start, buf = None, 0, []
    if cur:
        yield cur, start, "".join(buf)


def main() -> int:
    if not os.path.isdir(PKG):
        print(f"error: {PKG} is missing — nothing to check.", file=sys.stderr)
        return 1

    offenders, checked = [], 0
    for name in sorted(os.listdir(PKG)):
        if not name.endswith(".go") or name.endswith("_test.go"):
            continue
        path = os.path.join(PKG, name)
        for fn, line, body in functions(path):
            if not BRINGS_UP.search(body):
                continue
            checked += 1
            if fn in WAIVED or RECONCILES.search(body):
                continue
            offenders.append((os.path.join("internal", "api", name), line, fn))

    # A gate that checked nothing would also report success. Say what it saw.
    if checked == 0:
        print(
            "error: found no function that brings a container up. The call this "
            "gate keys on was renamed, and it has been passing vacuously.",
            file=sys.stderr,
        )
        return 1

    if offenders:
        print("Paths that bring a container up without reconciling co-located agents:\n", file=sys.stderr)
        for path, line, fn in offenders:
            print(f"  {path}:{line}  {fn}", file=sys.stderr)
        print(
            "\nThe entrypoint relaunches the PRIMARY agent only. Without a "
            "reconcile, every other agent in a multi-agent island comes back with "
            "no tmux session.\n"
            "\nFix: call s.reconcileAgentsAsync(p, ...) before returning. For the "
            "resume argument: ask when you did not bake the env in this same call "
            "(s.containerResumesPrimary), keep the literal when you did — see "
            "14a7ac1. If this path genuinely must not reconcile, add it to WAIVED "
            "with the reason.",
            file=sys.stderr,
        )
        return 1

    print(f"ok: all {checked} container-start paths in internal/api reconcile.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
