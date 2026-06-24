#!/usr/bin/env bash
# install-totals.sh — aggregate Dejima's PUBLIC download/install counts across the
# distribution channels that expose them: GitHub release assets, npm, and PyPI.
#
# WHY THIS EXISTS (and why it is NOT a server-side ping count):
#   The roadmap's first instinct was to "count the self-update pings server-side"
#   as an active-user proxy. That is impossible: the client's update check hits
#   api.github.com/repos/aoos/dejima/releases/latest DIRECTLY (internal/selfupdate),
#   so there is no Dejima-owned server that ever sees a ping. Standing up a Dejima
#   /version endpoint (the real active-user proxy) is deferred — "Option A".
#   This script is "Option B": it sums the download counts that are ALREADY PUBLIC.
#   It adds NO client telemetry, NO new client IDs, and changes NO client behavior.
#
# TWO CAVEATS THAT MUST NEVER BE FORGOTTEN (printed in every run):
#   1. HOMEBREW IS INVISIBLE. Installs via the `brew` tap have no public per-install
#      counter, so they are NOT in these totals. The real install count is HIGHER
#      than what you see here.
#   2. THESE ARE DOWNLOADS/INSTALLS, NOT ACTIVE USERS. One person may download many
#      times (CI, re-installs, mirrors); a download is not a retained user. The
#      active-user proxy is the deferred Option A, not this script.
#
# Usage:
#   scripts/metrics/install-totals.sh            # human-readable table (default)
#   scripts/metrics/install-totals.sh --json     # machine-readable JSON
#   scripts/metrics/install-totals.sh --help
#
# Exit:  0 = all reachable channels summed (a single unreachable channel is reported
#        inline and does not fail the run); 2 = bad usage; 3 = no channel reachable.
#
# Env (optional):
#   GITHUB_TOKEN / GH_TOKEN  — sent as a bearer token to lift the api.github.com
#                              unauthenticated rate limit. Read-only; never required.
#
# Public data sources (all unauthenticated, all aggregate):
#   GitHub : https://api.github.com/repos/aoos/dejima/releases   (sum assets[].download_count)
#   npm    : https://api.npmjs.org/downloads/point/last-month/<pkg>
#   PyPI   : https://pypistats.org/api/packages/<pkg>/recent     (last_month field)
set -uo pipefail

REPO="aoos/dejima"
NPM_PKGS=("dejima" "@dejima/sdk")     # CLI + SDK
PYPI_PKG="dejima-sdk"                 # the published SDK distribution name
UA="dejima-install-totals/1 (+https://github.com/aoos/dejima)"

JSON=0
for arg in "$@"; do
  case "$arg" in
    --json) JSON=1 ;;
    -h|--help)
      sed -n '2,46p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *) printf 'unknown argument: %s (try --help)\n' "$arg" >&2; exit 2 ;;
  esac
done

for bin in curl jq; do
  command -v "$bin" >/dev/null 2>&1 || { printf 'error: %s is required\n' "$bin" >&2; exit 2; }
done

# fetch <url> -> body on stdout, "" on any failure. Honors GITHUB_TOKEN for github.
fetch() {
  local url="$1" auth=()
  if [[ "$url" == https://api.github.com/* && -n "${GITHUB_TOKEN:-${GH_TOKEN:-}}" ]]; then
    auth=(-H "Authorization: Bearer ${GITHUB_TOKEN:-$GH_TOKEN}")
  fi
  curl -fsSL --max-time 30 -H "User-Agent: $UA" -H "Accept: application/json" "${auth[@]}" "$url" 2>/dev/null
}

# --- GitHub release assets -------------------------------------------------------
# Page through all releases; emit one JSON object per release with a per-asset
# breakdown and a release subtotal. Collected into GH_RELEASES (a JSON array) and
# GH_TOTAL (grand total of all assets[].download_count).
GH_RELEASES="[]"
GH_TOTAL=0
GH_OK=1
page=1
while :; do
  body="$(fetch "https://api.github.com/repos/$REPO/releases?per_page=100&page=$page")"
  if [[ -z "$body" ]]; then [[ "$page" -eq 1 ]] && GH_OK=0; break; fi
  count="$(jq 'length' <<<"$body" 2>/dev/null)" || { GH_OK=0; break; }
  [[ "${count:-0}" -eq 0 ]] && break
  page_json="$(jq '[ .[] | {
      tag: .tag_name,
      name: (.name // .tag_name),
      published_at: .published_at,
      assets: [ .assets[] | {name: .name, downloads: .download_count} ],
      release_total: ([ .assets[].download_count ] | add // 0)
    } ]' <<<"$body")"
  GH_RELEASES="$(jq -s '.[0] + .[1]' <<<"$GH_RELEASES"$'\n'"$page_json")"
  [[ "$count" -lt 100 ]] && break
  page=$((page + 1))
done
[[ "$GH_OK" -eq 1 ]] && GH_TOTAL="$(jq '[ .[].release_total ] | add // 0' <<<"$GH_RELEASES")"

# --- npm (last 30 days) ----------------------------------------------------------
# NPM_RESULTS: JSON array of {package, downloads}. NPM_TOTAL: sum. NPM_OK: any reached.
NPM_RESULTS="[]"
NPM_OK=0
for pkg in "${NPM_PKGS[@]}"; do
  body="$(fetch "https://api.npmjs.org/downloads/point/last-month/$pkg")"
  dl="$(jq -er '.downloads' <<<"${body:-}" 2>/dev/null)"
  if [[ -n "$dl" && "$dl" != "null" ]]; then
    NPM_OK=1
    NPM_RESULTS="$(jq --arg p "$pkg" --argjson d "$dl" '. + [{package:$p, downloads:$d}]' <<<"$NPM_RESULTS")"
  else
    NPM_RESULTS="$(jq --arg p "$pkg" '. + [{package:$p, downloads:null, error:"unreachable"}]' <<<"$NPM_RESULTS")"
  fi
done
NPM_TOTAL="$(jq '[ .[].downloads // 0 ] | add // 0' <<<"$NPM_RESULTS")"

# --- PyPI (last 30 days, via pypistats /recent .last_month) ----------------------
PYPI_OK=0
PYPI_TOTAL=0
body="$(fetch "https://pypistats.org/api/packages/$PYPI_PKG/recent")"
pm="$(jq -er '.data.last_month' <<<"${body:-}" 2>/dev/null)"
if [[ -n "$pm" && "$pm" != "null" ]]; then PYPI_OK=1; PYPI_TOTAL="$pm"; fi

# --- did anything succeed? -------------------------------------------------------
if [[ "$GH_OK" -eq 0 && "$NPM_OK" -eq 0 && "$PYPI_OK" -eq 0 ]]; then
  printf 'error: no channel was reachable (network/rate-limit?). Nothing to report.\n' >&2
  exit 3
fi

CAVEAT_BREW="Homebrew tap installs are NOT counted here — the brew channel exposes no public per-install number, so the true install count is higher than these totals."
CAVEAT_ACTIVES="These are downloads/installs, NOT active users. One person can download many times; a download is not a retained user. The active-user proxy is the deferred Option A, not this script."
WINDOW_NOTE="GitHub = lifetime (all-time per-asset); npm & PyPI = last 30 days."

if [[ "$JSON" -eq 1 ]]; then
  jq -n \
    --argjson gh_ok "$GH_OK" --argjson gh_total "${GH_TOTAL:-0}" --argjson gh_releases "$GH_RELEASES" \
    --argjson npm_ok "$NPM_OK" --argjson npm_total "${NPM_TOTAL:-0}" --argjson npm "$NPM_RESULTS" \
    --argjson pypi_ok "$PYPI_OK" --argjson pypi_total "${PYPI_TOTAL:-0}" --arg pypi_pkg "$PYPI_PKG" \
    --arg repo "$REPO" --arg window "$WINDOW_NOTE" \
    --arg brew "$CAVEAT_BREW" --arg actives "$CAVEAT_ACTIVES" \
    '{
      generated_at: (now | todateiso8601),
      window_note: $window,
      caveats: { homebrew_invisible: $brew, downloads_not_active_users: $actives },
      channels: {
        github_releases: { ok: ($gh_ok==1), repo: $repo, scope: "lifetime", total_downloads: $gh_total, releases: $gh_releases },
        npm:             { ok: ($npm_ok==1), scope: "last_30_days", total_downloads: $npm_total, packages: $npm },
        pypi:            { ok: ($pypi_ok==1), scope: "last_30_days", package: $pypi_pkg, total_downloads: $pypi_total },
        homebrew:        { ok: false, scope: "unmeasurable", total_downloads: null, note: $brew }
      },
      note_totals_not_comparable: "Channel scopes differ (GitHub lifetime vs npm/PyPI 30-day) — do not add the three grand totals into one number."
    }'
  exit 0
fi

# --- human-readable table --------------------------------------------------------
b() { printf '\033[1m%s\033[0m' "$*"; }
dim() { printf '\033[2m%s\033[0m' "$*"; }

printf '\n'
b "Dejima — public install / download totals"; printf '\n'
dim "$WINDOW_NOTE"; printf '\n\n'

# GitHub
b "GitHub release assets  "; dim "(github.com/$REPO · lifetime)"; printf '\n'
if [[ "$GH_OK" -eq 1 ]]; then
  jq -r '.[] | "  \(.tag)  \(.release_total) downloads"' <<<"$GH_RELEASES"
  printf '  %s %s\n' "$(b 'GitHub total:')" "$GH_TOTAL"
else
  printf '  (unreachable — network or api.github.com rate limit; set GITHUB_TOKEN)\n'
fi
printf '\n'

# npm
b "npm  "; dim "(api.npmjs.org · last 30 days)"; printf '\n'
jq -r '.[] | "  \(.package)  \(if .downloads == null then "(unreachable)" else (.downloads|tostring)+" downloads" end)"' <<<"$NPM_RESULTS"
printf '  %s %s\n\n' "$(b 'npm total:')" "$NPM_TOTAL"

# PyPI
b "PyPI  "; dim "(pypistats.org · last 30 days)"; printf '\n'
if [[ "$PYPI_OK" -eq 1 ]]; then
  printf '  %s  %s downloads\n' "$PYPI_PKG" "$PYPI_TOTAL"
else
  printf '  %s  (unreachable)\n' "$PYPI_PKG"
fi
printf '\n'

# Homebrew gap — always shown.
b "Homebrew  "; dim "(brew tap · UNMEASURABLE)"; printf '\n'
printf '  —  no public per-install counter exists for the tap.\n\n'

printf '%s\n' "$(b '── Caveats ──────────────────────────────────────────────')"
printf '  • %s\n' "$CAVEAT_BREW"
printf '  • %s\n' "$CAVEAT_ACTIVES"
printf '  • %s\n' "Scopes differ (GitHub lifetime vs npm/PyPI 30-day) — do not add the grand totals into one number."
printf '\n'
