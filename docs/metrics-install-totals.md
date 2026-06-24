# Install / download totals (Option B)

`scripts/metrics/install-totals.sh` aggregates Dejima's **already-public** download
counts across the distribution channels that expose them, and prints a per-channel
summary as a table (default) or JSON (`--json`).

```sh
scripts/metrics/install-totals.sh          # human-readable table
scripts/metrics/install-totals.sh --json   # machine-readable JSON
scripts/metrics/install-totals.sh --help
```

## Why "Option B" and not server-side ping counting

The roadmap (#10) first proposed counting the client's **self-update pings**
server-side as an active-user proxy. **That is impossible.** The client's update
check hits `https://api.github.com/repos/aoos/dejima/releases/latest` **directly**
(`internal/selfupdate`); it never contacts a Dejima-owned server, so there is no
ping for us to count. Standing up a Dejima `/version` endpoint — the real
active-user proxy — is **deferred ("Option A")**.

This script is **Option B**: it sums the download counts that are **already public**.
It adds **no** client telemetry, **no** new client IDs, and changes **no** client
behavior. It is pure read-only aggregation of public aggregate stats.

## Data sources

| Channel | Endpoint | Scope | Notes |
|---|---|---|---|
| **GitHub releases** | `GET api.github.com/repos/aoos/dejima/releases` → sum `assets[].download_count` | **Lifetime** (all-time, per asset) | Per-asset, per-release subtotal, and grand total. Unauthenticated calls are rate-limited to 60/hr; set `GITHUB_TOKEN`/`GH_TOKEN` to lift it. |
| **npm** | `GET api.npmjs.org/downloads/point/last-month/<pkg>` | **Last 30 days** | Packages: `dejima` (CLI) and `@dejima/sdk` (SDK). |
| **PyPI** | `GET pypistats.org/api/packages/dejima-sdk/recent` → `.data.last_month` | **Last 30 days** | Package: `dejima-sdk`. See "Why pypistats" below. |
| **Homebrew** | — | **Unmeasurable** | No public per-install count. See caveat 1. |

### Why pypistats for PyPI

PyPI does not expose a download count on its own package JSON
(`pypi.org/pypi/<pkg>/json`). The canonical public sources are the official
**BigQuery** `pypi.file_downloads` table (requires a GCP project + credentials —
too heavy for a one-line script) and **pypistats.org**, which is a free,
unauthenticated public mirror of exactly that BigQuery data. We use pypistats'
`/recent` endpoint because it is the simplest public source and its `last_month`
field lines up with npm's `last-month` window.

## Two caveats — printed on every run, repeated here so they're never lost

1. **Homebrew installs are invisible.** Installs via the `brew` tap have **no
   public per-install counter**, so they are **not** in these totals. The real
   install count is **higher** than what this script reports. Treat the numbers as
   a floor across the *measurable* channels, not "all installs."

2. **These are downloads/installs, NOT active users.** One person can download
   many times (CI, re-installs, package mirrors, mirroring bots); a download is not
   a retained user. The active-user proxy is the **deferred Option A**, not this
   script.

A third practical note: **channel scopes differ** — GitHub is lifetime, npm/PyPI
are 30-day — so the three grand totals are **not** comparable and must **not** be
summed into a single "installs" number. The script states this inline too.

## Behavior

- Each channel is fetched independently. A single unreachable channel (network
  blip or GitHub rate limit) is reported inline and does **not** fail the run.
- Exit codes: `0` = at least one channel summed; `2` = bad usage / missing
  `curl`/`jq`; `3` = no channel reachable at all.
- Dependencies: `curl` and `jq` (both already used elsewhere in this repo).
