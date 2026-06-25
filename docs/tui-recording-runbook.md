# TUI recording runbook (site motion clips B1–B4)

For d6's Track B — the site's hero + containment + quickstart clips. The TUI
ships a **demo mode** so these are reproducible and leak no real repos / paths /
secrets: `dejima --demo` (or `dejima tui --demo`) drives the dashboard from a
synthetic fleet (no daemon), and the agent states churn on their own so the
fleet looks alive. Recording is operator-on-host (a GUI terminal); this script
is the choreography.

## Setup (every clip)
- Terminal **120×32**, dark theme, large readable font. One scene per file.
- Deliver highest-quality raw: lossless / high-bitrate `.mov`/`.mp4`, ≥1280px
  wide. Encoding + looping happens site-side.
- Launch: `dejima --demo`. The fleet (storefront ×3 agents, api-gateway ×2,
  infra, hibernated docs-site) animates automatically — no live daemon needed.
- Demo control: **`!`** stages/unstages the action-gate scene (pending actions +
  the alert badge). It starts **off**, so the hero shot is clean.

## B1 — hero "aha" (~10–15s, must loop seamlessly)
The fleet dashboard: several agents across ≥2 islands, statuses churning
(working / needs-you / idle).
1. `dejima --demo` → land on the dashboard (approvals scene OFF).
2. Let it sit ~3s: agents visibly flip working↔needs-you↔idle on their own; the
   per-island color+glyph and the stats line (mem/cpu) tick.
3. `space` on `storefront` to expand its 3 agents (if not already), pause.
4. `↓` slowly across a couple of islands so the colored identity glyphs read.
- Loop tip: start and end on the collapsed all-islands view so it cuts clean.

## B2 — containment money-shot (~12–18s)
deny → broker → ledger. Two parts (the ledger cut is a real CLI):
1. In `--demo`, press **`!`** → the announcement bar turns **red**: "⚖ N
   cross-island action(s) need approval — destructive! · [V] review".
2. Press **`V`** → the approvals overlay. Highlight the **destructive**
   `drop-database` row (bold red, ⚠). Press **`v`** to expand its full payload
   (topic + params) — "never approve blind".
3. Press **`d`** → the deny prompt (optional reason) and Enter, OR `a` on the
   benign row to show approve. The destructive row's typed-confirm is the point:
   it can't be rubber-stamped.
4. **Ledger cut (live, not demo):** in a real island/daemon, run
   `dejima audit --verify` and capture the hash-chained `link.*` ledger line for
   the decision. (Demo has no ledger; this cut needs a real run.)

## B3 — quickstart (~8–12s)
dejima → new island → pick repo + agent → running.
- Best captured **live** (the creator hits the daemon): `dejima` → `n` → pick a
  repo (or paste a URL) → choose an agent → launch → it appears running.
- In `--demo` you can show the **creator flow UI** (`n` opens the picker) for the
  look, but it won't actually launch — use a live daemon for the "running" beat.

## B4 — clean `dejima audit --verify` (still ok)
A real CLI shot on a daemon with some ledger history: run
`dejima audit --verify` and capture the chain-verified output. Not a TUI scene.

## Key reference (current bindings)
- `⏎` on an island → opens **all its agents** (each in a window); on an agent →
  that agent; on a headless agent → its logs.
- `$` → in-island `/workspace` shell · `H` → host terminal.
- `V` → action-gate approvals · `T` → grants (trust surface) · `A` → audit
  ledger · `P` → Port scopes · `i` → set island color/glyph.
- `space`/`E` expand · `p` group-by-repo · `m` actions menu · `?` help.
- Demo-only: `!` toggles the action-gate scene.

## What demo covers
The dashboard (B1), the approval prompt + overlay (B2 up to the ledger cut), and
the creator-flow look (B3). The **ledger verify** (`dejima audit --verify`) and
the **actually-launching** beat of B3 need a live daemon — capture those on a
real host. Everything else is synthetic and safe to show.
