#!/usr/bin/env bash
# Install-channel sanity check — the part of the Lane A "bulletproof install"
# gate that needs NO Docker and NO virgin Mac, so it runs in CI and at the head
# of scripts/integration.sh. (The end-to-end virgin-Mac proof is the human-run
# Lane 0 procedure documented in the Lane A PR.)
#
# It confirms the three install channels (curl|sh, brew, npm) and their scripts
# are internally coherent — the failures that brick a clean-Mac install first-try:
#   · the install scripts pass `bash -n` (no syntax error ships)
#   · the curl one-liner doesn't document scripts/install.sh (a 404; the file is
#     at the repo root, served at dejima.tech/install.sh)
#   · install.sh recovers from a partial checkout + fetches tags (version baking)
#   · install-client.sh verifies the extracted binary + keeps its checksum guard
#   · the committed Homebrew formula is byte-identical to its generator's output
#
# Usage:   scripts/install-channels-check.sh
# Exit:    0 = all green; non-zero = a channel is inconsistent.
#
# The needles below are LITERAL grep patterns that intentionally contain `$`,
# `${…}` and backticks (we're searching source files for those exact strings),
# so single quotes are correct and shell expansion must not happen — silence the
# "expressions don't expand in single quotes" note for the whole file.
# shellcheck disable=SC2016
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PASS=0 FAIL=0
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS + 1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; FAIL=$((FAIL + 1)); }
step() { printf '\n\033[1m• %s\033[0m\n' "$*"; }

# want <file> <literal-substring> <ok-desc> [bad-desc]
# asserts the literal substring is present in $ROOT/<file>.
want() {
  local file="$1" needle="$2" okmsg="$3" badmsg="${4:-missing: $2}"
  if grep -qF -- "$needle" "$ROOT/$file"; then ok "$okmsg"; else bad "$badmsg"; fi
}
# absent <file> <literal-substring> <ok-desc> <bad-desc>
absent() {
  local file="$1" needle="$2" okmsg="$3" badmsg="$4"
  if grep -qF -- "$needle" "$ROOT/$file"; then bad "$badmsg"; else ok "$okmsg"; fi
}

# 1. The scripts must at least parse.
step "install scripts parse (bash -n)"
for s in install.sh install-client.sh scripts/setup.sh scripts/gen-homebrew-formula.sh; do
  if bash -n "$ROOT/$s" 2>/dev/null; then ok "$s"; else bad "$s has a syntax error"; fi
done

# 2. curl one-liner path is the repo root, not scripts/install.sh (a 404).
step "curl one-liner points at the served path (repo-root install.sh)"
if [ -f "$ROOT/install.sh" ]; then ok "install.sh is at the repo root"; else bad "install.sh missing from repo root"; fi
if [ -e "$ROOT/scripts/install.sh" ]; then bad "scripts/install.sh exists — duplicate of the served path will drift"; else ok "no stray scripts/install.sh"; fi
if grep -REq 'githubusercontent\.com/aoos/dejima/[^ ]*scripts/install\.sh' "$ROOT/install.sh" "$ROOT/README.md" "$ROOT/docs/distribution.md" 2>/dev/null; then
  bad "a doc advertises curl of scripts/install.sh (404)"
else
  ok "no doc advertises the dead scripts/install.sh download path"
fi

# 3. install.sh interrupt/retry safety + version baking.
step "install.sh is interrupt/retry-safe and bakes a real version"
want   install.sh 'rm -rf "$SRC_DIR"'    "re-clones over a partial checkout" "no partial-checkout recovery (retry bricks)"
want   install.sh 'rev-parse --git-dir'  "validates an existing checkout"    "doesn't validate the checkout"
want   install.sh 'fetch --quiet --tags' "fetches tags (git describe --tags)" "fetch omits --tags → version baked as a hash"
absent install.sh 'git clone --quiet --depth 1' "full clone keeps tags" "shallow clone strips tags → hash version"
want   install.sh 'command -v make' "prereq guard: make" "missing prereq guard: make"
want   install.sh 'command -v curl' "prereq guard: curl" "missing prereq guard: curl"
want   install.sh 'command -v git'  "prereq guard: git"  "missing prereq guard: git"

# 4. install-client.sh extraction + checksum guards.
step "install-client.sh verifies its download"
want install-client.sh '[[ -f "$tmp/dejima" ]] || fail' "verifies the extracted binary" "no post-extract binary check"
want install-client.sh 'checksum mismatch' "keeps the checksum-mismatch guard" "lost the checksum guard"

# 5. Asset names agree across producer (Makefile) + consumers (scripts/npm/brew).
step "release asset names agree across all channels"
want Makefile         'dejima_$${ver}_$${os}_$${arch}.tar.gz'                     "Makefile produces the unix tarball name" "Makefile tarball name drifted"
want install-client.sh 'asset="dejima_${ver}_${os}_${arch}.tar.gz"'              "install-client.sh asks for it"           "install-client.sh asset name drifted"
want npm/install.js   'const asset = `dejima_${tag}_${plat}_${arch}.${ext}`;'    "npm builds the same name"                "npm asset name drifted"
want homebrew/dejima.rb 'dejima_v#{version}_darwin_arm64.tar.gz'                 "brew formula URL matches"                "brew formula asset URL drifted"

# 6. Committed Homebrew formula == its generator's output (single source of truth).
step "committed Homebrew formula matches its generator"
ver="$(sed -n 's/^[[:space:]]*version "\([^"]*\)".*/\1/p' "$ROOT/homebrew/dejima.rb" | head -1)"
if [ -z "$ver" ]; then
  bad "could not read the formula version"
else
  # Build a SHA256SUMS from the formula's own checksums so the generator renders
  # the identical file; a mismatch means the formula was hand-edited out of band.
  mapfile -t shas < <(grep -oE 'sha256 "[0-9a-f]{64}"' "$ROOT/homebrew/dejima.rb" | grep -oE '[0-9a-f]{64}')
  if [ "${#shas[@]}" -ne 4 ]; then
    bad "expected 4 sha256 lines in the formula, found ${#shas[@]}"
  else
    tmpd="$(mktemp -d)"; trap 'rm -rf "$tmpd"' EXIT
    {
      printf '%s  dejima_v%s_darwin_arm64.tar.gz\n' "${shas[0]}" "$ver"
      printf '%s  dejima_v%s_darwin_amd64.tar.gz\n' "${shas[1]}" "$ver"
      printf '%s  dejima_v%s_linux_arm64.tar.gz\n'  "${shas[2]}" "$ver"
      printf '%s  dejima_v%s_linux_amd64.tar.gz\n'  "${shas[3]}" "$ver"
    } > "$tmpd/SHA256SUMS"
    if bash "$ROOT/scripts/gen-homebrew-formula.sh" "$ver" "$tmpd/SHA256SUMS" > "$tmpd/dejima.rb" 2>/dev/null \
       && diff -q "$tmpd/dejima.rb" "$ROOT/homebrew/dejima.rb" >/dev/null 2>&1; then
      ok "homebrew/dejima.rb == gen-homebrew-formula.sh output for v$ver"
    else
      bad "homebrew/dejima.rb is NOT what the generator emits for v$ver — regenerate it"
    fi
  fi
fi

printf '\n'
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32minstall channels OK (%d checks)\033[0m\n' "$PASS"
  exit 0
fi
printf '\033[31minstall-channel check FAILED: %d of %d checks failed\033[0m\n' "$FAIL" "$((PASS + FAIL))" >&2
exit 1
