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

    # The operator must still SEE the install, so this tees rather than
    # redirecting silently. It makes stdout a pipe — the condition #341 misread
    # as "nobody is here" — which is safe only because lib/tty.sh answers that
    # question on /dev/tty.
    # A FIFO plus an explicit wait, NOT `exec > >(tee …)`.
    #
    # Process substitution gives no PID to wait for, so on a fast exit the shell
    # is gone before tee drains and the log keeps only this header — which is
    # precisely the run whose log matters most. It looked fine in testing because
    # the test exited normally after a couple of echoes; a script that fails two
    # lines in, or is torn down by a harness, loses everything.
    #
    # With a FIFO the writer is ours: hold its PID, close stdout on the way out,
    # and wait for it. tee then sees EOF and flushes before we exit.
    FIFO="$(mktemp -u)"
    if ! mkfifo "$FIFO" 2>/dev/null; then
        unset DEJIMA_INSTALL_LOG
        return 0
    fi
    tee -a "$DEJIMA_INSTALL_LOG" <"$FIFO" &
    DEJIMA_LOG_TEE=$!
    exec >"$FIFO" 2>&1
    rm -f "$FIFO"   # unlinked; both ends stay open until they are closed
    trap 'exec 1>&- 2>&-; wait "$DEJIMA_LOG_TEE" 2>/dev/null' EXIT
}

# transcript_note prints where the log is. Called on the failure paths, where it
# is the difference between a report and a guess.
transcript_note() {
    [[ -n "${DEJIMA_INSTALL_LOG:-}" ]] || return 0
    printf '\n  A full transcript of this run is at:\n    %s\n' "$DEJIMA_INSTALL_LOG"
    printf '  Send that file — it says what actually happened, in order.\n'
}
