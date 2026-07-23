#!/usr/bin/env bash
# load-secrets.sh — export the island's Dejima-managed secrets into this shell.
#
# Usage:  eval "$(/opt/dejima/load-secrets.sh)"   (from the shell profile)
#
# The daemon writes /opt/host/secrets.env as one NAME=value per line, values
# percent-escaped for newline and '%'. This script PARSES that file and emits
# shell-safe `export` statements. It never sources it.
#
# That distinction is the whole point. Sourcing a file makes bash EXECUTE what
# it reads, so a token containing a backtick or $(...) would run as a command in
# every new shell — turning "add a secret" into arbitrary code execution. Here
# the value is only ever data: read as text, unescaped by parameter expansion
# (no subshell, no eval), and re-quoted for output.
#
# Emitting export lines rather than exporting directly is what lets a profile
# apply them to its own shell; the caller evals OUR output, which we control and
# quote, not the file's contents, which we don't.
set -euo pipefail

SECRETS_FILE="${DEJIMA_SECRETS_FILE:-/opt/host/secrets.env}"
[[ -r "$SECRETS_FILE" ]] || exit 0


while IFS= read -r line || [[ -n "$line" ]]; do
    # Skip blanks and the header comments.
    [[ -z "$line" || "$line" == \#* ]] && continue
    # Split on the FIRST '=' only: names cannot contain '=', values may.
    name="${line%%=*}"
    value="${line#*=}"
    [[ "$name" == "$line" ]] && continue   # no '=' at all — malformed, skip

    # Re-validate the name here rather than trusting the file. The daemon
    # already rejects reserved names, but this file is a mount: defence in depth
    # costs one test and closes the gap if anything ever writes it directly.
    [[ "$name" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    # nocasematch rather than ${name^^}: that expansion needs bash 4, and this
    # script should stay runnable on a bash 3.2 host (macOS) as well as in the
    # island's bash 5.
    shopt -s nocasematch
    skip=0
    case "$name" in
        LD_*|DYLD_*|GLIBC_*|BASH_*|DEJIMA_*) skip=1 ;;
        PATH|HOME|USER|LOGNAME|SHELL|ENV|IFS|PS4|SHELLOPTS|CDPATH) skip=1 ;;
        HTTP_PROXY|HTTPS_PROXY|ALL_PROXY|NO_PROXY) skip=1 ;;
    esac
    shopt -u nocasematch
    [[ $skip -eq 1 ]] && continue

    # Unescape via parameter expansion only — no eval, no subshell, so the value
    # is never interpreted. Order matters: %25 last, so a literal "%25" in the
    # original doesn't get double-decoded.
    value="${value//%0A/$'\n'}"
    value="${value//%0D/$'\r'}"
    value="${value//%25/%}"

    # %q is bash's own "quote this so it is reusable as shell input". Hand-rolled
    # single-quote escaping is easy to get subtly wrong (an earlier version was),
    # and getting it wrong is precisely the injection this file exists to stop.
    # Let the shell quote for the shell.
    printf 'export %s=%q\n' "$name" "$value"
done < "$SECRETS_FILE"
