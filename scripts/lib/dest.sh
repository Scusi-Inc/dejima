#!/usr/bin/env bash
# The installer's destination decision: local, host, or client.
#
# Sourced by scripts/setup.sh AFTER lib/tty.sh — it uses have_tty, TTY_FD and
# fail from there. Split out so scripts/lib/dest_test.sh can drive it under a
# real pty without running an installer: one of the three answers is "nobody is
# there", and that answer is the one with consequences for every scripted
# caller, so it has to be exercised rather than reasoned about.
#
# Prints exactly one of: local | host | client — on STDOUT, which is the return
# value. The menu goes to /dev/tty, so a caller can capture the answer without
# capturing the question.

setup_destination() {
    case "${DEJIMA_SETUP:-}" in
        local|host|client) printf '%s' "${DEJIMA_SETUP}"; return 0 ;;
        "") ;;
        # fail() PRINTS, it does not exit — so the exit is explicit here. Without
        # it the case falls through, the function returns nothing, and a typo'd
        # DEJIMA_SETUP silently becomes a host install: the one outcome a
        # scripted caller spelling out its intent must never get by accident.
        # The exit ends the $(…) subshell; set -e in the caller takes the
        # failed assignment from there.
        *)
            fail "DEJIMA_SETUP must be local, host or client (got '${DEJIMA_SETUP}')"
            exit 1
            ;;
    esac
    # NON-INTERACTIVE RUNS STAY ON HOST. Nobody to ask means nobody to surprise
    # either, but every existing runbook, CI job and doc that pipes this script
    # expects the host behaviour, and silently making those local would take
    # remote access off machines whose owners never saw a prompt. Scripted
    # callers that want local say so: DEJIMA_SETUP=local.
    if ! have_tty; then
        printf 'host'
        return 0
    fi
    {
        printf '\n\033[1m%s\033[0m\n\n' "How do you want to run Dejima on this machine?"
        printf '    l) Local  — just for you, on this machine\n'
        printf '               (Docker + the daemon; no tailnet, nothing always-on)\n'
        printf '    h) Host   — an always-on server for a team, reachable from your other devices\n'
        printf '               (adds Tailscale; this machine stays awake)\n'
        printf '    c) Client — a server already exists and you have an invite\n'
        printf '               (no build needed — you want the CLI from a package manager)\n\n'
        printf 'Choice [L/h/c]: '
    } >/dev/tty
    local reply
    read -r -u "$TTY_FD" reply || reply=""
    # Bare Enter is Local: it is the least destructive answer, and the one an
    # operator who did not read the menu most likely wanted. An unrecognised key
    # lands there too rather than guessing at a commitment — `h` provisions a
    # machine, and that is not a thing to do on a typo.
    case "${reply:-l}" in
        h|H|host)   printf 'host' ;;
        c|C|client) printf 'client' ;;
        *)          printf 'local' ;;
    esac
}
