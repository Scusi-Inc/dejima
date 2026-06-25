# Dejima public download totals — daily snapshots

Data-only branch. `install-totals.jsonl` is one JSON object per line (JSONL),
appended once a day by `.github/workflows/metrics.yml` (on master) from
`scripts/metrics/install-totals.sh --json`. GitHub = lifetime; npm/PyPI = rolling
last-30-days (so the daily series is the only way to see launch-window trends —
the rolling windows can't be backfilled). Homebrew is unmeasurable (not counted).
