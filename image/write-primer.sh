#!/usr/bin/env bash
# write-primer.sh — install the Dejima island primer into an agent's GLOBAL
# instructions file as an idempotent managed block.
#
# Usage: write-primer.sh <target-file>
#   e.g. write-primer.sh "$HOME/.claude/CLAUDE.md"   (Claude Code)
#        write-primer.sh "$HOME/.codex/AGENTS.md"    (Codex)
#
# The block is delimited by BEGIN/END markers so it is:
#   - non-clobbering: content the operator/agent added outside the block is kept;
#   - self-refreshing: an existing block is REPLACED (not duplicated) each run, so
#     rebuilding the image / restarting the container updates the primer text.
# The <island> placeholder is filled from $DEJIMA_PROJECT_NAME. Best-effort: the
# caller should tolerate a non-zero exit (a primer write must never crash a
# container).
set -euo pipefail

target="${1:?usage: write-primer.sh <target-file>}"
template="${PRIMER_TEMPLATE:-/opt/dejima/island-primer.md}"
begin="<!-- BEGIN dejima island primer (managed — edits inside are overwritten) -->"
end="<!-- END dejima island primer -->"

[ -f "$template" ] || { echo "write-primer: template not found: $template" >&2; exit 1; }

island="${DEJIMA_PROJECT_NAME:-this island}"
# Render: substitute the island name. Island names pass ValidateName (no regex
# metacharacters), so a literal sed replace is safe.
rendered="$(sed "s|<island>|${island}|g" "$template")"
block="${begin}
${rendered}
${end}"

mkdir -p "$(dirname "$target")"
tmp="${target}.dejima.tmp"

# Drop any existing managed block first (keep everything else), then (re)append the
# fresh one at the end. This is portable across GNU + BSD awk — the awk vars are
# single-line marker strings only; the multi-line block is never passed through
# awk -v (BSD awk rejects a newline in a -v value).
if [ -f "$target" ] && grep -qF "$begin" "$target"; then
    awk -v b="$begin" -v e="$end" '
        $0 == b { skip = 1; next }
        $0 == e { skip = 0; next }
        !skip   { print }
    ' "$target" >"$tmp" && mv "$tmp" "$target"
fi

if [ -s "$target" ]; then
    printf '\n%s\n' "$block" >>"$target" # append after the operator's/agent's own content
else
    printf '%s\n' "$block" >"$target" # fresh (or block-only) file
fi
