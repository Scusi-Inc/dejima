#!/usr/bin/env bash
# The codex agent's self-repair for a missing platform binary.
#
# codex ships its executable as an OPTIONAL per-platform npm dependency, and
# `npm install -g` SUCCEEDS when an optional dependency fails — npm's designed
# behaviour. So an image builds green with a codex that dies on first use:
#
#   Error: Missing optional dependency @openai/codex-linux-arm64.
#
# An operator hit that. Nothing at agent-creation time could have fixed it,
# because the agent uses the image rather than installing anything.
#
# Driven with stub `codex` and `npm` on PATH, so the repair branch is exercised
# without an npm registry. Asserting on the script's text would only restate it.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
PASS=0; FAIL=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL+1)); }

# run_init <broken-until-reinstall?> — returns the init script's stderr
run_init() {
    local heals="$1"
    local bin home
    bin="$(mktemp -d)"; home="$(mktemp -d)"
    local flag="$bin/repaired"

    # codex: fails until npm "repairs" it, if this case is meant to heal.
    cat > "$bin/codex" <<CODEX
#!/bin/sh
[ -f "$flag" ] && { echo "codex-cli 1.0.0"; exit 0; }
echo "Error: Missing optional dependency @openai/codex-linux-arm64" >&2
exit 1
CODEX
    case "$heals" in
        yes)     printf '#!/bin/sh\ntouch %s\nexit 0\n' "$flag" > "$bin/npm" ;;
        # THE CASE THAT MATTERS MOST, and the original bug exactly: npm EXITS 0
        # while the platform binary is still missing. That is npm's designed
        # behaviour for a failed optional dependency, and it is why "npm
        # succeeded" cannot be taken as "codex works".
        lying)   printf '#!/bin/sh\nexit 0\n' > "$bin/npm" ;;
        *)       printf '#!/bin/sh\nexit 1\n' > "$bin/npm" ;;
    esac
    chmod +x "$bin/codex" "$bin/npm"

    HOME="$home" PATH="$bin:/usr/bin:/bin" \
        bash "$ROOT/image/agents/codex/init.sh" 2>&1 || true
}

echo "codex agent: missing platform binary repairs itself"

out="$(run_init yes)"
if grep -q "reinstalling" <<<"$out"; then
    ok "detects the missing binary and attempts a reinstall"
else
    bad "never noticed the binary was missing:
$out"
fi
if grep -q "codex reinstalled" <<<"$out"; then
    ok "confirms the repair by re-checking, not by assuming npm worked"
else
    bad "reports nothing about whether the repair took:
$out"
fi

# When it cannot repair, it must NOT fail the agent — a codex that starts and
# complains beats an island that refuses to come up — and it must name the fix.
out="$(run_init no)"
if grep -q "npm install -g @openai/codex@latest" <<<"$out"; then
    ok "unrepairable: hands over the exact command"
else
    bad "unrepairable and says nothing actionable:
$out"
fi
if grep -qi "could not repair" <<<"$out"; then
    ok "unrepairable: says so plainly"
else
    bad "silent about the failure:
$out"
fi

# npm exits 0 and the binary is STILL missing — npm's actual behaviour for a
# failed optional dependency, and the bug this whole thing exists for. Trusting
# npm's exit status here would report a repair that did not happen.
out="$(run_init lying)"
if grep -q "codex reinstalled" <<<"$out"; then
    bad "claimed a repair on npm's exit status alone — npm exits 0 for a FAILED
       optional dependency, which is the original bug:
$out"
else
    ok "npm exiting 0 is not taken as proof; the binary is re-checked"
fi
if grep -q "npm install -g @openai/codex@latest" <<<"$out"; then
    ok "still hands over the command when npm lied"
else
    bad "npm lied and the operator is told nothing:
$out"
fi

echo
if [[ "$FAIL" -eq 0 ]]; then
    printf '\033[1mPASS — %d checks.\033[0m\n' "$PASS"
else
    printf '\033[1mFAIL — %d of %d failed.\033[0m\n' "$FAIL" "$((PASS+FAIL))"
fi
[[ "$FAIL" -eq 0 ]]
