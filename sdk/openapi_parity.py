#!/usr/bin/env python3
"""Route-parity check: every daemon route ⇔ every documented operation.

`openapi.yaml` is hand-maintained from the route table in `internal/api/*.go`
(the source of truth). This catches drift in both directions:

  - a route registered in Go but missing from the spec (undocumented endpoint), and
  - an operation in the spec with no matching Go route (stale/typo'd path).

Path parameters are canonicalized to a placeholder, so `{name}` vs `{island}` or
`{path...}` vs `{path}` never trips a false mismatch — only verb + structure
matter. Run from the repo root (or pass --repo). Exits non-zero on any mismatch.
"""

from __future__ import annotations

import argparse
import os
import re
import sys

try:
    import yaml
except ImportError:  # pragma: no cover
    # Hard failure (exit non-zero), never a silent pass: a missing dependency must
    # fail CI loudly rather than let the parity gate false-pass.
    print("error: PyYAML is required (pip install pyyaml)", file=sys.stderr)
    raise SystemExit(2)

# Matches `mux.HandleFunc("GET /v1/path", ...)` in the Go sources.
ROUTE_RE = re.compile(r'HandleFunc\(\s*"([A-Z]+)\s+(/[^"]*)"')
METHODS = {"get", "post", "put", "patch", "delete", "head", "options", "trace"}


def canon(path: str) -> str:
    """Collapse every `{param}` / `{param...}` to `{}` so param names don't matter."""
    return re.sub(r"\{[^}]*\}", "{}", path)


def go_routes(api_dir: str) -> set[tuple[str, str]]:
    routes: set[tuple[str, str]] = set()
    for entry in sorted(os.listdir(api_dir)):
        if not entry.endswith(".go") or entry.endswith("_test.go"):
            continue
        with open(os.path.join(api_dir, entry), encoding="utf-8") as fh:
            # Skip comment lines. A doc comment that QUOTES the pattern — which
            # any explanation of this gate naturally does — otherwise injects a
            # phantom route into the report. That is not hypothetical: it was
            # reported by one agent and then done, within the hour, by the
            # person fixing the thing they reported. The comment explaining the
            # blind spot created a fake route named `VERB /path`.
            #
            # Line-based rather than a Go parser: this file is deliberately
            # dependency-light, and a leading `//` is the only case that has
            # ever produced a false route here.
            body = "\n".join(
                line for line in fh.read().splitlines()
                if not line.lstrip().startswith("//")
            )
            for verb, path in ROUTE_RE.findall(body):
                routes.add((verb.upper(), canon(path)))
    return routes


def spec_routes(spec_path: str) -> set[tuple[str, str]]:
    with open(spec_path, encoding="utf-8") as fh:
        spec = yaml.safe_load(fh)
    out: set[tuple[str, str]] = set()
    for path, ops in (spec.get("paths") or {}).items():
        for method in ops:
            if method.lower() in METHODS:
                out.add((method.upper(), canon(path)))
    return out


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", default=os.getcwd(), help="repo root (default: cwd)")
    args = ap.parse_args()

    api_dir = os.path.join(args.repo, "internal", "api")
    spec_path = os.path.join(args.repo, "openapi.yaml")
    if not os.path.isdir(api_dir):
        sys.exit(f"no Go API dir at {api_dir} (run from the repo root, or pass --repo)")

    go = go_routes(api_dir)
    spec = spec_routes(spec_path)

    missing_from_spec = sorted(go - spec)
    missing_from_go = sorted(spec - go)

    if missing_from_spec:
        print("Routes in internal/api/*.go but NOT in openapi.yaml:")
        for verb, path in missing_from_spec:
            print(f"  - {verb} {path}")
    if missing_from_go:
        print("Operations in openapi.yaml with NO matching Go route:")
        for verb, path in missing_from_go:
            print(f"  - {verb} {path}")

    if missing_from_spec or missing_from_go:
        print(f"\nFAIL: {len(missing_from_spec)} undocumented, {len(missing_from_go)} stale.")
        return 1
    print(f"OK: openapi.yaml matches all {len(go)} daemon routes.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
