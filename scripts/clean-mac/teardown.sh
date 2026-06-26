#!/usr/bin/env bash
# Clean-Mac teardown — reset the box to a virgin Dejima environment.
#
# Idempotent: safe on an already-clean box. Removes every trace of a prior Dejima
# install for the CURRENT user — ~/.dejima, ~/.dejima-src, the user-prefix
# binaries, the test island's docker objects, and the brew/npm package — so the
# next install starts virgin. This is the "teardown" half of the launch-gate
# proof loop; proof-loop.sh calls it before EACH channel.
#
# It does NOT touch /usr/local, system launchd, or (by default) colima's VM. For
# a from-zero Docker VM as well, pass --reset-colima (CLEANMAC_RESET_COLIMA=1) —
# destructive to every container on this user's colima.
#
# Usage:
#   scripts/clean-mac/teardown.sh [--reset-colima] [--assert-virgin]
# Refuses to run as `aoos` (it deletes ~/.dejima); run as the dejimaqa test user.
SUITE_NAME="clean-mac-teardown"
# shellcheck source=scripts/clean-mac/lib.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

ASSERT=0
for arg in "$@"; do
  case "$arg" in
    --reset-colima) export CLEANMAC_RESET_COLIMA=1 ;;
    --assert-virgin) ASSERT=1 ;;
    -h|--help) grep '^#' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown arg: $arg" ;;
  esac
done

refuse_as_aoos
require_macos_or_note
hdr "TEARDOWN → virgin Dejima environment"
reset_colima
teardown_dejima
[ "$ASSERT" -eq 1 ] && assert_virgin

report_and_exit
