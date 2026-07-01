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
if [ ! -f "$target" ]; then
    printf '%s\n' "$block" >"$target"
elif grep -qF "$begin" "$target"; then
    # Replace the existing block in place (BEGIN..END inclusive), keep the rest.
    awk -v b="$begin" -v e="$end" -v blk="$block" '
        $0 == b { print blk; skip = 1; next }
        $0 == e { skip = 0; next }
        !skip   { print }
    ' "$target" >"${target}.dejima.tmp" && mv "${target}.dejima.tmp" "$target"
else
    # Append after the existing content, separated by a blank line.
    printf '\n%s\n' "$block" >>"$target"
fi
