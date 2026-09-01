#!/usr/bin/env bash
# Record the install to a file, so a failure produces EVIDENCE rather than a
# paraphrase.
#
# The fresh-Mac install failed in the field three times and each round cost
# several messages reconstructing what happened from memory and a screenshot —
# "docker install or something timed out" was the entire evidence for one of
# them. The clean-Mac launch gate that would have caught these needs a virgin
# macOS host, and the only one was torn down, so the people hitting this ARE the
# test. The least we can do is have them able to send the log.
#
# start_transcript is idempotent across the install.sh → `make setup` handoff:
# install.sh exports DEJIMA_INSTALL_LOG and `exec`s into make, so setup.sh
# inherits both the variable and the redirected file descriptors and must not
# wrap them a second time.
#
# WHAT IS NOT IN THE LOG: passwords. sudo reads them from /dev/tty with echo
# off, so they never pass through stdout — which is also why the tty split in
# #341 mattered, and why prompts still reach a human after this redirect makes
# stdout a pipe.

start_transcript() {
    [[ -n "${DEJIMA_INSTALL_LOG:-}" ]] && return 0

    local dir="${HOME:-/tmp}"
    DEJIMA_INSTALL_LOG="$dir/dejima-install-$(date +%Y%m%d-%H%M%S).log"
    export DEJIMA_INSTALL_LOG

    if ! : >"$DEJIMA_INSTALL_LOG" 2>/dev/null; then
        # Not fatal. An install that works without a log beats one that refuses
        # to start because it could not open one.
        unset DEJIMA_INSTALL_LOG
        return 0
    fi

    # A self-describing header, because the first question about any install log
    # is "which machine, which version, when".
    {
        printf 'dejima install transcript\n'
        printf 'date:    %s\n' "$(date)"
        printf 'host:    %s\n' "$(uname -a 2>/dev/null || echo unknown)"
        printf 'shell:   %s\n' "${BASH_VERSION:-unknown}"
        printf 'ref:     %s\n' "${DEJIMA_REF:-master}"
        printf '\n'
    } >>"$DEJIMA_INSTALL_LOG"

    # tee, not a plain redirect: the operator must still SEE the install. This
    # makes stdout a pipe, which is exactly the condition that used to be read as
    # "nobody is here" — lib/tty.sh answers that on /dev/tty instead, so prompts
    # and the sudo pre-authorization still find the human.
    exec > >(tee -a "$DEJIMA_INSTALL_LOG") 2>&1
}

# transcript_note prints where the log is. Called on the failure paths, where it
# is the difference between a report and a guess.
transcript_note() {
    [[ -n "${DEJIMA_INSTALL_LOG:-}" ]] || return 0
    printf '\n  A full transcript of this run is at:\n    %s\n' "$DEJIMA_INSTALL_LOG"
    printf '  Send that file — it says what actually happened, in order.\n'
}
