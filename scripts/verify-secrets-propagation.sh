#!/usr/bin/env bash
# Does a secret change actually reach a running island?
#
# Run this ON THE DAEMON HOST, as the operator. It cannot be run from inside an
# island by design: `dejima upgrade` is denied to island tokens and islands have
# no Docker socket, so the one protocol nobody had run is also the one no agent
# CAN run. This script exists to make the operator's single run cheap and
# unambiguous instead of a page of prose to follow by hand.
#
#   scripts/verify-secrets-propagation.sh <island>
#
# What it settles, which reasoning could not:
#
#   RUN 1   does a write reach a running container at all
#   RUN 2   does REPLACING an existing value reach it   <- the case never tested
#   REVOKE  does `secret rm` actually remove it         <- the one that fails silently
#
# Background. The daemon used to bind-mount the secrets FILE. A file bind binds
# the INODE, and the file is rewritten with CreateTemp+Rename — a NEW inode — so
# a container read the original for its whole life while the daemon reported
# success. The fix mounts the DIRECTORY. Every content-level check passed
# throughout the bug's life, because the daemon always wrote the right bytes to
# the right path; only propagation into a live container distinguishes them.
#
# `dejima exec` is the probe rather than an agent restart, deliberately: it
# starts a NEW login shell in the SAME container, which isolates "is the mount
# live" from "does an already-running process see it". A running process keeps
# its launch-time environment either way — that is not the bug and never was.

set -uo pipefail

ISLAND="${1:-}"
if [[ -z "$ISLAND" ]]; then
    echo "usage: $0 <island>" >&2
    exit 2
fi

# NOT a DEJIMA_-prefixed name: image/load-secrets.sh deliberately skips
# DEJIMA_*, LD_*, PATH and friends, so a probe named that way would report a
# propagation failure that is really the safety filter doing its job.
PROBE="SECRET_PROPAGATION_PROBE"
V1="probe-value-one"
V2="probe-value-two"

PASS=0
FAIL=0
bold() { printf '\033[1m%s\033[0m\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; PASS=$((PASS + 1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; FAIL=$((FAIL + 1)); }
info() { printf '    %s\n' "$*"; }

# Read the probe from a NEW login shell inside the island. Login shell because
# secrets arrive via /etc/profile.d, which a non-login shell never sources.
# ${VAR-UNSET} (no colon) and ${VAR:-EMPTY} say different things, and the
# difference decides where to look: UNSET means load-secrets.sh never exported
# it, EMPTY means it did and the value is blank — a `--stdin` that read nothing.
# The first version of this used ${VAR:-ABSENT}, which collapses both into one
# word and cost a whole field round-trip to un-collapse.
read_probe() {
    dejima exec "$ISLAND" -- bash -lc \
        "v=\${${PROBE}-UNSET}; [ -z \"\$v\" ] && v=EMPTY; printf '%s' \"\$v\"" 2>/dev/null
}

# on_read_failure prints what the CONTAINER can see, so a bad read is diagnosed
# in the same run instead of costing a round trip. The env and the file are
# different facts: in-file-but-not-in-env is the parser or the profile hook;
# not-in-file is the daemon.
on_read_failure() {
    # Re-read with the SIMPLEST possible form, immediately.
    #
    # The first field run reported a specific defect in code that did not have
    # it, and the second could not be reproduced: the script's read said UNSET
    # while the same read by hand, seconds later, returned the value. Every
    # difference I could test — the command string, the variable name, the
    # timing, the call sequence — came out identical. So the script now captures
    # the discriminator itself instead of costing another round trip:
    #
    #   both disagree  -> the two read forms differ, and that is the bug
    #   both agree     -> the earlier read was transient; the value arrived late
    local simple
    simple="$(dejima exec "$ISLAND" -- bash -lc "printf '%s' \"\${${PROBE}-UNSET}\"" 2>/dev/null)"
    info "re-read with the simple form: '${simple}'"
    if [[ "$simple" != "$got" ]]; then
        info "  THE TWO READ FORMS DISAGREE ('$got' vs '$simple') — that is this"
        info "  script's bug, not the daemon's. Report both values."
    else
        info "  both read forms agree, so the read is consistent — the value was"
        info "  genuinely not in the environment at that moment."
    fi
    info "what the island can actually see:"
    local line
    line="$(dejima exec "$ISLAND" -- grep -c "^${PROBE}=" /opt/host/secrets.d/secrets.env 2>/dev/null || echo 0)"
    if [[ "$line" == "0" ]]; then
        info "  ${PROBE} is NOT in secrets.d/secrets.env — the daemon never wrote it."
        info "  Look at the daemon, not the island: is it new enough to refresh on set?"
    else
        info "  ${PROBE} IS in secrets.d/secrets.env but did not reach the shell."
        info "  Look in the island: /opt/dejima/load-secrets.sh and"
        info "  /etc/profile.d/10-dejima-secrets.sh. Run the first by hand to see its output."
    fi
}

cleanup() {
    dejima secret rm "$ISLAND" "$PROBE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

bold "Verifying secret propagation into '$ISLAND'"
echo

# --- Preflight ------------------------------------------------------------
# Which mount does this container actually have? A container created before the
# fix still carries the old file mount and CANNOT pass — reporting that up front
# turns a confusing failure into a one-line answer.
bold "0. Can we reach the island at all?"
# Asked separately, and first, because every probe below runs through `dejima
# exec`: without this, an island that is stopped, misspelled or nonexistent
# fails the mount check and gets told its container predates the fix. That is a
# diagnosis naming the wrong cause, which is the failure this whole script
# exists to stop being possible.
if ! exec_err="$(dejima exec "$ISLAND" -- true 2>&1)"; then
    bad "cannot run a command in '$ISLAND'"
    info "${exec_err:-(no error output)}"
    info "Check the name and that it is running: dejima ls"
    exit 1
fi
ok "'$ISLAND' is reachable"
echo

bold "1. Which mount does this container have?"
if dejima exec "$ISLAND" -- test -d /opt/host/secrets.d 2>/dev/null; then
    ok "/opt/host/secrets.d is present — this container has the directory mount"
else
    bad "/opt/host/secrets.d is ABSENT — this container predates the secrets-mount fix"
    info "It cannot propagate a change, and that is expected, not a new bug."
    info "Fix it on the host, in this order — the image rebuild is NOT optional:"
    info "  git pull && make install && make image && dejima service restart"
    info "  dejima upgrade $ISLAND"
    info "Skipping the rest: a failure here would only re-report the known state."
    exit 1
fi
if dejima exec "$ISLAND" -- test -r /opt/host/secrets.d/secrets.env 2>/dev/null; then
    ok "secrets.env is readable through the directory mount"
else
    bad "secrets.env is NOT readable inside the mount — nothing below can pass"
fi
echo

# --- RUN 1 ----------------------------------------------------------------
bold "2. Does a write reach a running container at all?"
set_out="$(printf '%s' "$V1" | dejima secret set "$ISLAND" "$PROBE" --stdin 2>&1)" \
    || bad "dejima secret set failed: $set_out"
got="$(read_probe)"
if [[ "$got" == "$V1" ]]; then
    ok "a newly-set secret is visible in a new login shell"
else
    bad "set did not propagate — read '$got', want '$V1'"
    info "daemon said: $(printf '%s' "$set_out" | head -1)"
    on_read_failure
fi
echo

# --- RUN 2 ----------------------------------------------------------------
# The case that had never been run. Set-then-read can pass on a stale mount if
# the value happened to be written before the container was created; REPLACING
# a value cannot.
bold "3. Does REPLACING an existing value reach it?"
set_out="$(printf '%s' "$V2" | dejima secret set "$ISLAND" "$PROBE" --stdin 2>&1)" \
    || bad "dejima secret set (rotate) failed: $set_out"
got="$(read_probe)"
if [[ "$got" == "$V2" ]]; then
    ok "a rotated secret is visible in a new login shell"
elif [[ "$got" == "$V1" ]]; then
    bad "ROTATION DID NOT PROPAGATE — still reading the ORIGINAL value"
    info "This is the file-inode bug's exact signature: the write succeeded on the"
    info "host and the container is still resolving the pre-rename inode."
else
    bad "rotate did not propagate — read '$got', want '$V2'"
    info "daemon said: $(printf '%s' "$set_out" | head -1)"
    on_read_failure
fi
echo

# --- REVOKE ---------------------------------------------------------------
bold "4. Does 'secret rm' actually remove it?"
dejima secret rm "$ISLAND" "$PROBE" >/dev/null 2>&1 || bad "dejima secret rm failed"
got="$(read_probe)"
if [[ "$got" == "UNSET" ]]; then
    ok "a removed secret is gone from a new login shell"
else
    bad "REVOKE DID NOT PROPAGATE — the value is still readable as '$got'"
    info "This is the worst of the three. A set that does not propagate fails"
    info "loudly and soon: the agent's tool errors and someone investigates. A rm"
    info "that does not propagate fails silently and permanently — nothing errors,"
    info "and the only symptom is a revoked credential that still works."
fi
echo

# --- Result ---------------------------------------------------------------
if [[ "$FAIL" -eq 0 ]]; then
    bold "PASS — $PASS checks. Propagation into a running island is verified."
    echo "  This is the protocol that had been reasoned about but never run."
else
    bold "FAIL — $FAIL of $((PASS + FAIL)) checks failed."
    echo "  Capture this output verbatim. A summary drops exactly the detail that"
    echo "  distinguishes 'stale value' from 'no value', and those have different causes."
fi
[[ "$FAIL" -eq 0 ]]
