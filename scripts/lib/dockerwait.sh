#!/usr/bin/env bash
# Waiting for Docker Desktop's first launch — the part that talks to the
# operator while nothing is happening yet.
#
# Split out of scripts/setup.sh because it cannot be tested in place: the loop
# it belongs to runs only on a Mac that has just had Docker Desktop installed,
# and by then the sentence it prints IS the installer's entire user interface
# for the next five minutes.
#
# What it got wrong on a Mac mini install: `open -a Docker` returned success
# without bringing anything up, and the loop then said "CHECK THIS MAC'S
# SCREEN: first launch shows a licence agreement that must be accepted" every
# 30 seconds at a screen with nothing on it. The operator sat through the full
# wait before the timeout finally distinguished "running, waiting for a click"
# from "never started" — which is the distinction that decides what to do, and
# the loop already had everything it needed to make it.

# docker_cli_ok — is the Docker CLI talking to a daemon?
#
# The braces and the OUTER redirect are load-bearing, and are not the same as
# the redirect on `docker version` itself. When the CLI is SIGKILLed, the
# "Killed: 9" notice is printed by THIS shell about its child, so the child's
# own redirect cannot catch it; only a redirect around the shell's execution of
# the command can. Docker Desktop rewrites the CLI binaries under ~/.docker/bin
# during first launch and macOS kills a running image whose code signature no
# longer matches the file on disk — harmless, and it happens at most once, but
# it put
#
#   scripts/setup.sh: line 334: 34601 Killed: 9   docker version > /dev/null 2>&1
#
# on the screen of an install that was working correctly, directly above the
# line asking the operator to keep waiting.
docker_cli_ok() {
    { docker version >/dev/null 2>&1; } 2>/dev/null
}

# docker_desktop_running — is there a Docker Desktop process at all? Both forms
# matter: the app bundle has been named both ways across versions.
docker_desktop_running() {
    pgrep -f "Docker Desktop" >/dev/null 2>&1 || pgrep -x Docker >/dev/null 2>&1
}

# console_user — who is logged in at this Mac's physical display. Empty when
# nobody is (the login window owns the console as root), when it can't be read,
# or off macOS.
#
# Screen Sharing into a logged-in account reports that account, which is the
# answer we want: that session can show a licence dialog and a person can click
# it. An SSH session cannot, and does not appear here.
console_user() {
    [[ "$(uname -s)" == "Darwin" ]] || return 0
    local who=""
    who="$(stat -f%Su /dev/console 2>/dev/null)" || return 0
    [[ "$who" == "root" ]] && return 0
    printf '%s' "$who"
}

# docker_wait_advice <running:0|1> <console-user> <me>
#
# Prints what to do about the current state, one instruction per line. Pure:
# it inspects no processes, so the caller decides when to ask and the answer is
# testable without a Mac.
#
# The two axes are independent and each has its own fix. Whether Docker Desktop
# is RUNNING decides between "accept the prompt" and "launch it by hand".
# Whether a GUI session exists decides whether a prompt can be seen at all —
# and if it can't, no amount of waiting will help, which is the case the old
# loop could not say out loud.
docker_wait_advice() {
    local running="$1" console="$2" me="$3"

    if [[ -z "$console" ]]; then
        # Nobody at the display: a licence dialog has nowhere to appear, and
        # nobody can click it. This is the state a headless mac-mini install
        # sits in forever.
        if [[ "$running" == "1" ]]; then
            printf '%s\n' "Docker Desktop is running, but nobody is logged in at this Mac's display."
        else
            printf '%s\n' "Docker Desktop is not running, and nobody is logged in at this Mac's display."
        fi
        printf '%s\n' "Its licence prompt can only appear in a logged-in desktop session, so waiting will not clear this."
        printf '%s\n' "Log in at the Mac (or Screen Sharing) as ${me}, then open Docker."
        return 0
    fi

    if [[ "$console" != "$me" ]]; then
        # A different account owns the display. Launching from here puts the
        # prompt in a session this operator isn't looking at.
        printf '%s\n' "This Mac's display is logged in as ${console}, but the installer is running as ${me}."
        printf '%s\n' "Docker Desktop's first-launch prompt appears in ${console}'s session, not ${me}'s."
        printf '%s\n' "Log in at the display as ${me}, or finish Docker's first launch there as ${console}."
        return 0
    fi

    if [[ "$running" == "1" ]]; then
        printf '%s\n' "Docker Desktop IS running — it is waiting for you ON THIS MAC'S SCREEN."
        printf '%s\n' "First launch shows a licence agreement and asks to install a privileged helper."
        printf '%s\n' "The daemon does not exist until both are accepted."
        return 0
    fi

    # Running as the console user, and nothing came up. `open` reporting
    # success means the launch request was accepted, not that the app started —
    # so this is the one that has to say "click it yourself".
    printf '%s\n' "Docker Desktop is NOT running — the launch did not take (\`open -a Docker\` reports success either way)."
    printf '%s\n' "Open it by hand: click Docker in /Applications or Launchpad, on this Mac's screen."
    printf '%s\n' "If macOS refuses to open it, it is quarantined:"
    printf '%s\n' "  xattr -dr com.apple.quarantine /Applications/Docker.app"
}

# docker_relaunch — one more launch attempt, by bundle path.
#
# `open -a Docker` resolves the name through LaunchServices, which can hand
# back a stale or shadowed registration and still exit 0; the path form cannot.
# Its own function so the test can replace it — otherwise running the suite on
# a Mac would launch Docker Desktop.
docker_relaunch() {
    [[ -d /Applications/Docker.app ]] || return 0
    open -a /Applications/Docker.app 2>/dev/null || true
}

# docker_wait_for_daemon [max-seconds]
#
# Waits for the Docker daemon, narrating the wait. Returns 0 as soon as the CLI
# answers, 1 on timeout.
#
# Needs info/warn from scripts/lib/tty.sh, which setup.sh sources first (the
# test stubs them). Every nudge re-reads the state rather than repeating what
# was true at second one: "accept the prompt on screen" and "the app never
# launched" are opposite instructions, and which one applies can change while
# this loop is running — including because the operator acted on the last line
# it printed.
docker_wait_for_daemon() {
    local max="${1:-300}" i dd_running=0 said_not_running=0 line
    for (( i = 1; i <= max; i++ )); do
        if docker_cli_ok; then return 0; fi
        if docker_desktop_running; then dd_running=1; else dd_running=0; fi

        # `open` exits 0 when the launch REQUEST is accepted, not when the app
        # comes up, so a launch that quietly did nothing looks identical to one
        # that worked. Fifteen seconds is long enough for a real launch to show
        # a process and short enough that nobody has to sit through the whole
        # five-minute wait being told to watch an empty screen — which is
        # exactly what happened before this branch existed.
        if (( i == 15 && dd_running == 0 && said_not_running == 0 )); then
            said_not_running=1
            warn "Docker Desktop has not started"
            docker_relaunch
            while IFS= read -r line; do
                info "  $line"
            done < <(docker_wait_advice 0 "$(console_user)" "$(whoami)")
        fi

        # A silent multi-minute pause is indistinguishable from a hang — which
        # is what prompted a Ctrl-C last time, and the Ctrl-C is what left
        # Docker.app half-linked.
        if (( i % 30 == 0 )); then
            info "  …still waiting (${i}s)"
            while IFS= read -r line; do
                info "     $line"
            done < <(docker_wait_advice "$dd_running" "$(console_user)" "$(whoami)")
        fi
        sleep 1
    done
    return 1
}
