#!/usr/bin/env python3
"""Field-parity check: every json tag ⇔ every documented property, every query
param read ⇔ every documented parameter.

`openapi_parity.py` next door checks ROUTES — verb + canonicalized path. That is
a real gate and it is only half the surface. A route can be documented while the
body it accepts, the body it returns, and the query params it reads have drifted
arbitrarily far from the Go structs behind them, and nothing goes red. That is
how `no_repo` (a create-time flag with a deliberate opt-in contract) and `force`
on agent removal (a destructive override) both reached shipping clients while the
spec never mentioned them.

Two checks live here:

  1. SCHEMAS. Every struct in `internal/api` whose name matches a schema in
     `components.schemas` is compared field-by-field with that schema, and the
     comparison follows the shape transitively: a field whose type is another
     struct is compared against the schema its property `$ref`s. That transitive
     step is what reaches types OUTSIDE internal/api (`ledger.Entry` ⇔
     `LedgerEntry`, `mailbox.Message` ⇔ `MailboxMessage`, …) without needing a
     hand-maintained name table — the binding is read out of the spec itself.

  2. QUERY PARAMS. Every `r.URL.Query().Get("x")` a handler reaches — directly,
     or through a helper it hands `r` or `r.URL.Query()` to — must be documented
     as an `in: query` parameter on that operation.

Both directions fail: a json tag with no property is an undocumented field, and a
property with no json tag is a stale one a client will wait forever to receive.

WHAT THIS DELIBERATELY DOES NOT CHECK (so a green run isn't read as more than it
is — see docs/testing/guards-need-controls.md):

  - Types, formats, and enum VALUES. `state: {type: string}` satisfies this check
    whatever the Go field's type is. Only names are compared.
  - Required-ness. Most schemas here carry no `required` list at all, so
    enforcing `omitempty` ⇔ `required` would be a rewrite, not a check.
  - Request/response bodies documented INLINE rather than as a named schema
    (moveAgent's `{delta}`, the terminal label bodies). Nothing binds those to a
    Go type; they are small and hand-checked.
  - Schemas describing a wire shape with no Go struct behind it at all.
  The count of Go structs it could NOT bind is printed on every run, so the
  uncovered set stays visible instead of quietly growing.

Run from the repo root (or pass --repo). Exits non-zero on any mismatch.
`--self-test` runs the mutation control (below) instead of the check.
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile

try:
    import yaml
except ImportError:  # pragma: no cover
    # Hard failure (exit non-zero), never a silent pass: a missing dependency must
    # fail CI loudly rather than let the parity gate false-pass.
    print("error: PyYAML is required (pip install pyyaml)", file=sys.stderr)
    raise SystemExit(2)

MODULE = "github.com/aoos/dejima/"

# Tripwires. These are floors, not targets: the check's whole failure mode is
# scanning nothing and reporting all-clear, and "0 structs parsed" reads exactly
# like "no drift". Bump them deliberately when the surface genuinely shrinks.
MIN_STRUCTS = 200  # structs parsed out of internal/
MIN_BOUND = 90  # (Go struct ⇔ schema) pairs actually compared
MIN_FIELDS = 500  # individual field names compared
MIN_ROUTES = 80  # routes read out of the mux registrations
MIN_QUERY_READS = 10  # query params found being read by handlers

STRUCT_RE = re.compile(r"^type\s+(\w+)\s+struct\s*\{", re.M)
IMPORT_BLOCK_RE = re.compile(r"^import\s*\(([^)]*)\)", re.M)
IMPORT_ONE_RE = re.compile(r'^import\s+(?:(\w+)\s+)?"([^"]+)"', re.M)
IMPORT_LINE_RE = re.compile(r'^\s*(?:(\w+|\.|_)\s+)?"([^"]+)"\s*$')
PACKAGE_RE = re.compile(r"^package\s+(\w+)", re.M)
ROUTE_RE = re.compile(r'HandleFunc\(\s*"([A-Z]+)\s+([^"]*)"\s*,\s*(?:s\.)?(\w+)')
FUNC_RE = re.compile(r"^func\s+(?:\([^)]*\)\s*)?(\w+)\s*\(", re.M)
QUERY_GET_RE = re.compile(r'r\.URL\.Query\(\)\.(?:Get|Has)\(\s*"([^"]+)"\s*\)')
BARE_GET_RE = re.compile(r'(?<![\w.])(?:get|Get)\(\s*"([^"]+)"\s*\)')
QMAP_RE = re.compile(r'q\[\s*"([^"]+)"\s*\]')
METHODS = {"get", "post", "put", "patch", "delete", "head", "options", "trace"}


# ---------------------------------------------------------------- Go parsing


def _matching_brace(src: str, open_idx: int) -> int:
    """Index of the `}` closing the `{` at open_idx. Brace-counting is enough
    here: struct bodies in this tree carry no braces inside string literals."""
    depth = 0
    for j in range(open_idx, len(src)):
        if src[j] == "{":
            depth += 1
        elif src[j] == "}":
            depth -= 1
            if depth == 0:
                return j
    return len(src) - 1


def parse_go_file(path: str, rel_dir: str) -> dict:
    src = open(path, encoding="utf-8").read()
    pkg_m = PACKAGE_RE.search(src)
    imports: dict[str, str] = {}
    block = IMPORT_BLOCK_RE.search(src)
    lines = block.group(1).splitlines() if block else []
    for one in IMPORT_ONE_RE.finditer(src):
        lines.append(f'{one.group(1) or ""} "{one.group(2)}"')
    for line in lines:
        m = IMPORT_LINE_RE.match(line)
        if not m:
            continue
        alias, ipath = m.group(1), m.group(2)
        imports[alias or ipath.rsplit("/", 1)[-1]] = ipath

    structs: dict[str, list] = {}
    for m in STRUCT_RE.finditer(src):
        open_idx = src.index("{", m.start())
        body = src[open_idx + 1 : _matching_brace(src, open_idx)]
        fields = []
        for line in body.splitlines():
            line = line.split("//")[0].strip()
            if not line:
                continue
            tag_m = re.search(r'`[^`]*json:"([^"]*)"', line)
            decl = re.sub(r"`[^`]*`", "", line).strip()
            if tag_m:
                parts = tag_m.group(1).split(",")
                name = parts[0]
                if name == "-":
                    continue
                type_m = re.match(r"^(\w+)\s+(.+)$", decl)
                if not type_m:
                    # A tagged EMBEDDED field (`Foo \`json:"foo"\``): the decl is
                    # just the type, and the tag names it as an ordinary field.
                    fields.append((name, decl, "omitempty" in parts[1:], False))
                    continue
                fields.append((name, type_m.group(2).strip(), "omitempty" in parts[1:], False))
            else:
                # Embedded field: a bare (possibly qualified, possibly pointer)
                # type name on its own line. Its fields are inlined into the JSON.
                if re.fullmatch(r"\*?(?:\w+\.)?[A-Za-z_]\w*", decl):
                    fields.append((None, decl, False, True))
        structs[m.group(1)] = fields

    return {
        "package": pkg_m.group(1) if pkg_m else "",
        "imports": imports,
        "structs": structs,
        "dir": rel_dir,
        "src": src,
    }


def index_go(repo: str) -> dict:
    """Index every non-test Go file under internal/ by (dir, TypeName)."""
    structs: dict[tuple[str, str], list] = {}
    file_imports: dict[tuple[str, str], dict[str, str]] = {}
    for root, _dirs, files in os.walk(os.path.join(repo, "internal")):
        rel_dir = os.path.relpath(root, repo).replace(os.sep, "/")
        for f in sorted(files):
            if not f.endswith(".go") or f.endswith("_test.go"):
                continue
            info = parse_go_file(os.path.join(root, f), rel_dir)
            for name, fields in info["structs"].items():
                structs[(rel_dir, name)] = fields
                file_imports[(rel_dir, name)] = info["imports"]
    return {"structs": structs, "imports": file_imports}


def peel_go(t: str) -> tuple[str, list[str]]:
    """Strip pointer/slice/map wrappers, returning the base type and the wrappers
    peeled (outermost first) so the schema side can be peeled in step."""
    wrappers = []
    while True:
        t = t.strip()
        if t.startswith("*"):
            t = t[1:]
            continue
        if t.startswith("[]"):
            wrappers.append("array")
            t = t[2:]
            continue
        m = re.match(r"^map\[[^\]]+\](.*)$", t)
        if m:
            wrappers.append("map")
            t = m.group(1)
            continue
        return t, wrappers


def resolve_go_type(idx: dict, holder: tuple[str, str], type_expr: str):
    """Resolve a field's type to a (dir, TypeName) struct key, or None."""
    base, wrappers = peel_go(type_expr)
    if "." in base:
        qual, name = base.split(".", 1)
        ipath = idx["imports"].get(holder, {}).get(qual)
        if not ipath or not ipath.startswith(MODULE):
            return None, wrappers
        key = (ipath[len(MODULE) :], name)
    else:
        key = (holder[0], base)
    return (key if key in idx["structs"] else None), wrappers


# -------------------------------------------------------------- spec walking


def schema_object(spec: dict, node, seen=None):
    """Flatten a schema node to {property: subschema}, following $ref/allOf.

    Returns (properties, resolvable) — resolvable is False when a $ref pointed at
    a schema that doesn't exist, so the caller can refuse to conclude anything
    rather than compare against an accidentally-empty set."""
    seen = seen or set()
    props: dict = {}
    ok = True
    if not isinstance(node, dict):
        return props, ok
    if "$ref" in node:
        name = node["$ref"].rsplit("/", 1)[-1]
        if name in seen:
            return props, ok
        seen = seen | {name}
        target = (spec.get("components", {}).get("schemas") or {}).get(name)
        if target is None:
            return props, False
        return schema_object(spec, target, seen)
    for key in ("allOf", "anyOf", "oneOf"):
        for sub in node.get(key) or []:
            sub_props, sub_ok = schema_object(spec, sub, seen)
            props.update(sub_props)
            ok = ok and sub_ok
    props.update(node.get("properties") or {})
    return props, ok


def peel_schema(spec: dict, node, wrappers: list[str]):
    """Peel `items`/`additionalProperties` in step with the Go wrappers."""
    for w in wrappers:
        if not isinstance(node, dict):
            return None
        if "$ref" in node:
            name = node["$ref"].rsplit("/", 1)[-1]
            node = (spec.get("components", {}).get("schemas") or {}).get(name)
            if node is None:
                return None
        if w == "array":
            node = node.get("items")
        else:
            node = node.get("additionalProperties")
        if not isinstance(node, dict):
            return None
    return node


# ------------------------------------------------------------- schema parity


class SchemaCheck:
    def __init__(self, repo: str):
        self.repo = repo
        self.idx = index_go(repo)
        with open(os.path.join(repo, "openapi.yaml"), encoding="utf-8") as fh:
            self.spec = yaml.safe_load(fh)
        self.schemas = self.spec.get("components", {}).get("schemas") or {}
        self.problems: list[str] = []
        self.bound: set[tuple] = set()
        self.fields_compared = 0
        self.unbound_api_structs: list[str] = []

    def go_fields(self, key, seen=None):
        """(tag -> type_expr, holder) for a struct, embeds inlined. The second
        return value is False when an embed couldn't be resolved — the field set
        is then INCOMPLETE, and concluding "the spec documents a field Go doesn't
        have" from it would be an accusation drawn from our own blind spot."""
        seen = seen or set()
        if key in seen:
            return {}, True
        seen = seen | {key}
        out: dict[str, tuple[str, tuple]] = {}
        complete = True
        for name, type_expr, _omit, embedded in self.idx["structs"].get(key, []):
            if embedded:
                sub, sub_ok = None, False
                resolved, _w = resolve_go_type(self.idx, key, type_expr)
                if resolved:
                    sub, sub_ok = self.go_fields(resolved, seen)
                if sub is None:
                    complete = False
                    continue
                out.update(sub)
                complete = complete and sub_ok
                continue
            out[name] = (type_expr, key)
        return out, complete

    def compare(self, go_key, schema_node, label: str):
        marker = (go_key, id(schema_node))
        if marker in self.bound:
            return
        self.bound.add(marker)

        props, resolvable = schema_object(self.spec, schema_node)
        if not resolvable:
            self.problems.append(f"{label}: schema $ref points at a schema that does not exist")
            return
        fields, complete = self.go_fields(go_key)

        for tag in sorted(set(fields) - set(props)):
            self.problems.append(
                f"{label}: Go field `json:\"{tag}\"` ({go_key[0]}.{go_key[1]}) "
                f"is not documented as a property"
            )
        if complete:
            for prop in sorted(set(props) - set(fields)):
                self.problems.append(
                    f"{label}: documented property `{prop}` has no json tag on "
                    f"{go_key[0]}.{go_key[1]} (stale — clients will never see it)"
                )
        self.fields_compared += len(set(fields) | set(props))

        for tag in sorted(set(fields) & set(props)):
            type_expr, holder = fields[tag]
            sub_key, wrappers = resolve_go_type(self.idx, holder, type_expr)
            if not sub_key:
                continue
            sub_schema = peel_schema(self.spec, props[tag], wrappers)
            if sub_schema is None:
                continue
            sub_props, _ = schema_object(self.spec, sub_schema)
            if not sub_props:
                continue  # opaque/untyped on the spec side; nothing to compare
            self.compare(sub_key, sub_schema, f"{label}.{tag}")

    def run(self):
        api = "internal/api"
        for name in sorted(self.schemas):
            if (api, name) in self.idx["structs"]:
                self.compare((api, name), self.schemas[name], name)
        for (d, name), fields in sorted(self.idx["structs"].items()):
            if d != api or not fields or not name[0].isupper():
                continue  # unexported types are never a wire shape
            if not any(k[0] == (d, name) for k in self.bound):
                self.unbound_api_structs.append(name)


# --------------------------------------------------------- query-param parity


def func_bodies(repo: str) -> dict[str, str]:
    """name -> body source, for every func/method in internal/api."""
    out: dict[str, str] = {}
    api = os.path.join(repo, "internal", "api")
    for f in sorted(os.listdir(api)):
        if not f.endswith(".go") or f.endswith("_test.go"):
            continue
        src = open(os.path.join(api, f), encoding="utf-8").read()
        for m in FUNC_RE.finditer(src):
            brace = src.find("{", m.end())
            if brace < 0:
                continue
            out[m.group(1)] = src[brace : _matching_brace(src, brace) + 1]
    return out


def query_params_read(bodies: dict[str, str], entry: str) -> set[str]:
    """Params the handler reads, following helpers it hands `r` or the parsed
    query to. Without that hop the check would go blind exactly where the code is
    most parameterized — parseAuditFilter/parseActivityFilter take the query map,
    and serveTmuxWS takes the whole request."""
    found: set[str] = set()
    seen: set[str] = set()
    # (func, reached-with-the-query-map-rather-than-the-request)
    queue = [(entry, False)]
    while queue:
        fn, query_mode = queue.pop()
        if (fn, query_mode) in seen or fn not in bodies:
            continue
        seen.add((fn, query_mode))
        body = bodies[fn]
        found |= set(QUERY_GET_RE.findall(body))
        if query_mode:
            # Reached holding the query map: a `get("x")` closure or a `q["x"]`
            # index is a param read here, the same as Query().Get would be.
            found |= set(BARE_GET_RE.findall(body))
            found |= set(QMAP_RE.findall(body))
        for m in re.finditer(r"(?<![\w.])(?:s\.)?(\w+)\(([^()]*(?:\([^()]*\)[^()]*)*)\)", body):
            callee, args = m.group(1), m.group(2)
            arglist = [a.strip() for a in args.split(",")]
            if "r.URL.Query()" in arglist:
                queue.append((callee, True))
            elif "r" in arglist:
                queue.append((callee, False))
    return found


def documented_params(spec: dict, path_item: dict, op: dict) -> set[str]:
    names: set[str] = set()
    for source in ((path_item or {}).get("parameters") or [], (op or {}).get("parameters") or []):
        for p in source:
            if "$ref" in p:
                ref = p["$ref"].rsplit("/", 1)[-1]
                p = (spec.get("components", {}).get("parameters") or {}).get(ref) or {}
            if p.get("in") == "query":
                names.add(p.get("name"))
    return names


class QueryCheck:
    def __init__(self, repo: str):
        self.repo = repo
        with open(os.path.join(repo, "openapi.yaml"), encoding="utf-8") as fh:
            self.spec = yaml.safe_load(fh)
        self.bodies = func_bodies(repo)
        self.routes: list[tuple[str, str, str]] = []
        api = os.path.join(repo, "internal", "api")
        for f in sorted(os.listdir(api)):
            if not f.endswith(".go") or f.endswith("_test.go"):
                continue
            src = open(os.path.join(api, f), encoding="utf-8").read()
            self.routes += [(v.upper(), p, h) for v, p, h in ROUTE_RE.findall(src)]
        self.problems: list[str] = []
        self.reads = 0

    def run(self):
        paths = self.spec.get("paths") or {}
        for verb, path, handler in sorted(self.routes):
            params = query_params_read(self.bodies, handler)
            self.reads += len(params)
            item = paths.get(path)
            if item is None:
                continue  # a routing mismatch: openapi_parity.py owns that
            op = item.get(verb.lower())
            if op is None:
                continue
            documented = documented_params(self.spec, item, op)
            for p in sorted(params - documented):
                self.problems.append(
                    f"{verb} {path}: handler reads query param `{p}` "
                    f"({handler}) — not documented as an `in: query` parameter"
                )


# ---------------------------------------------------------------- the runner


def check(repo: str):
    sc = SchemaCheck(repo)
    sc.run()
    qc = QueryCheck(repo)
    qc.run()
    return sc, qc


def report(sc: SchemaCheck, qc: QueryCheck) -> int:
    problems = sc.problems + qc.problems
    # Non-emptiness controls. A source-scanning check fails OPEN: rename what it
    # matches, move the files it reads, and it reports "all clear" over an empty
    # set. Each of these separates a different way of seeing nothing — reading no
    # files, binding no types, and comparing no fields are three distinct
    # failures that would otherwise print the same reassuring line.
    tripwires = [
        (len(sc.idx["structs"]), MIN_STRUCTS, "Go structs parsed from internal/"),
        (len(sc.bound), MIN_BOUND, "(struct ⇔ schema) pairs compared"),
        (sc.fields_compared, MIN_FIELDS, "field names compared"),
        (len(qc.routes), MIN_ROUTES, "routes read from the mux registrations"),
        (qc.reads, MIN_QUERY_READS, "query-param reads found in handlers"),
    ]
    blind = [
        f"only {got} {what} (floor {floor}) — this check has stopped watching; "
        f"fix the scanner or lower the floor deliberately"
        for got, floor, what in tripwires
        if got < floor
    ]

    if problems:
        print("Field-level drift between internal/api and openapi.yaml:")
        for p in problems:
            print(f"  - {p}")
    if blind:
        print("\nThe check itself is not seeing:")
        for b in blind:
            print(f"  - {b}")
    if problems or blind:
        print(f"\nFAIL: {len(problems)} drift, {len(blind)} blind-spot.")
        return 1
    # Counted as (Go struct ⇔ schema SITE) pairs, not distinct schemas: a type
    # reached both directly and through another schema's `items` is compared at
    # both sites. Named for what it is rather than rounded up to a flattering
    # "N schemas" — an instrument that overstates itself is not a control.
    print(
        f"OK: {len(sc.bound)} (Go struct ⇔ schema) comparisons clean "
        f"({sc.fields_compared} field names), and every query param the "
        f"{len(qc.routes)} daemon routes read is documented."
    )
    if sc.unbound_api_structs:
        print(
            f"note: {len(sc.unbound_api_structs)} internal/api structs have no "
            f"same-named schema and are NOT covered: "
            f"{', '.join(sc.unbound_api_structs)}"
        )
    return 0


# ----------------------------------------------------------- mutation control


def _mutate_text(path: str, old: str, new: str) -> None:
    """Replace `old` with `new`, asserting it matched EXACTLY ONE site.

    "Something changed" is not enough: a pattern that matches twice mutates the
    wrong place just as happily, and the survived-mutation that follows reads as
    "the guard is fine" or, worse, as a defect in someone else's code."""
    s = open(path, encoding="utf-8").read()
    if s.count(old) != 1:
        raise SystemExit(f"MUTATION AMBIGUOUS: {old!r} matched {s.count(old)} sites in {path}")
    open(path, "w", encoding="utf-8").write(s.replace(old, new))


def _rewrite_spec(path: str, mutate) -> None:
    """Round-trip openapi.yaml through a structural edit.

    Structural rather than textual on purpose: `- name: force` appears on two
    operations and `no_hibernate` on two schemas, so a regex would mutate a site
    the control is not testing and then report an honest, misdirected zero. The
    edit is made on the parsed tree, where "the property I meant" is addressable.
    `mutate` must raise if the thing it removes was not there."""
    spec = yaml.safe_load(open(path, encoding="utf-8"))
    mutate(spec)
    with open(path, "w", encoding="utf-8") as fh:
        yaml.safe_dump(spec, fh, allow_unicode=True, sort_keys=False)


def _drop_property(schema: str, prop: str):
    def go(spec):
        props = spec["components"]["schemas"][schema]["properties"]
        if prop not in props:
            raise SystemExit(f"MUTATION DID NOT APPLY: {schema} has no property {prop}")
        del props[prop]

    return go


def _drop_query_param(path: str, verb: str, name: str):
    def go(spec):
        op = spec["paths"][path][verb]
        keep = [p for p in op.get("parameters", []) if p.get("name") != name]
        if len(keep) != len(op.get("parameters", [])) - 1:
            raise SystemExit(f"MUTATION DID NOT APPLY: {verb} {path} has no {name} parameter")
        op["parameters"] = keep

    return go


def _go_parses(path: str) -> bool:
    """A mutant that doesn't parse gives a red that looks like the guard working."""
    gofmt = shutil.which("gofmt") or "/usr/local/go/bin/gofmt"
    if not os.path.exists(gofmt):
        print("    gofmt unavailable — cannot confirm the Go mutant still parses")
        return False
    p = subprocess.run([gofmt, "-e", "-l", path], capture_output=True, text=True)
    if p.returncode != 0:
        print(f"    mutant does not parse: {p.stderr.strip()}")
        return False
    return True


def _fresh_copy(repo: str, tmp: str) -> str:
    work = os.path.join(tmp, "repo")
    os.makedirs(work)
    shutil.copy(os.path.join(repo, "openapi.yaml"), os.path.join(work, "openapi.yaml"))
    shutil.copytree(os.path.join(repo, "internal"), os.path.join(work, "internal"))
    return work


def self_test(repo: str) -> int:
    """Prove the check can see a failure before anyone trusts its silence.

    Each mutation runs on a THROWAWAY COPY of an unmutated tree, one change at a
    time — a control that keeps a second change in flight gives a confident
    answer to a different question. Every case first asserts the copy passes
    CLEAN, so the red that follows can only be the mutation; the spec cases
    additionally assert the YAML round-trip alone (no mutation) still passes, so
    a re-dump artifact can't be mistaken for the guard firing."""
    failures = []
    cases = [
        (
            "a documented property removed from a schema",
            _drop_property("IslandInfo", "no_hibernate"),
            None,
            'json:"no_hibernate"',
        ),
        (
            "a documented query param removed from an operation",
            _drop_query_param("/v1/islands/{name}/agents/{id}", "delete", "force"),
            None,
            "query param `force`",
        ),
        (
            "a json tag removed from a Go struct",
            None,
            (
                "internal/api/types.go",
                '\tNoRepo bool `json:"no_repo,omitempty"`\n\t// Role is',
                "\t// Role is",
            ),
            "documented property `no_repo`",
        ),
    ]

    for what, spec_mut, go_mut, expect in cases:
        print(f"control: {what}")
        with tempfile.TemporaryDirectory() as tmp:
            work = _fresh_copy(repo, tmp)

            base_sc, base_qc = check(work)
            if base_sc.problems or base_qc.problems:
                failures.append(f"{what}: the UNMUTATED copy already fails — the control is not isolating")
                for p in (base_sc.problems + base_qc.problems)[:5]:
                    print(f"    baseline problem: {p}")
                continue
            before = (len(base_sc.bound), base_sc.fields_compared)

            spec_path = os.path.join(work, "openapi.yaml")
            if spec_mut is not None:
                _rewrite_spec(spec_path, lambda s: None)  # round-trip, no mutation
                rt_sc, rt_qc = check(work)
                if rt_sc.problems or rt_qc.problems:
                    failures.append(f"{what}: the YAML round-trip ALONE fails — a red here would not be the mutation")
                    continue
                _rewrite_spec(spec_path, spec_mut)
            if go_mut is not None:
                rel, old, new = go_mut
                _mutate_text(os.path.join(work, rel), old, new)
                if not _go_parses(os.path.join(work, rel)):
                    failures.append(f"{what}: the mutant no longer parses — the red would not be the guard")
                    continue

            sc, qc = check(work)
            after = (len(sc.bound), sc.fields_compared)
            if after[0] < before[0] * 0.9:
                failures.append(
                    f"{what}: the mutation collapsed the scan ({before} → {after}) rather than removing one field"
                )
                continue
            hits = [p for p in sc.problems + qc.problems if expect in p]
            if not hits:
                failures.append(f"{what}: SURVIVED — the check did not report it")
                print(f"    (reported instead: {sc.problems + qc.problems})")
                continue
            print(f"    caught: {hits[0]}")

    if failures:
        print("\nSELF-TEST FAILED:")
        for f in failures:
            print(f"  - {f}")
        return 1
    print(f"\nOK: the check caught all {len(cases)} mutations.")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--repo", default=os.getcwd(), help="repo root (default: cwd)")
    ap.add_argument(
        "--self-test",
        action="store_true",
        help="run the mutation control (does the check still see a removed field?)",
    )
    args = ap.parse_args()

    api_dir = os.path.join(args.repo, "internal", "api")
    if not os.path.isdir(api_dir):
        sys.exit(f"no Go API dir at {api_dir} (run from the repo root, or pass --repo)")

    if args.self_test:
        return self_test(args.repo)
    return report(*check(args.repo))


if __name__ == "__main__":
    raise SystemExit(main())
