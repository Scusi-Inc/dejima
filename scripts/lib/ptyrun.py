#!/usr/bin/env python3
"""Run a script with a controlling terminal while stdin is wired independently.

Exists because the case that broke the fresh-Mac install (#341) cannot be
produced by running a script the ordinary way: a person is at the keyboard, so
/dev/tty is open and writable, but stdin is the `curl` pipe, so `[[ -t 0 ]]` is
false. Any harness that just pipes into a script loses the terminal too, and any
harness that runs it interactively loses the pipe. Both halves have to be set
independently, which needs a real pty.

Usage:
    ptyrun.py <mode> <script> [args...]      # test input on stdin, echoed to pty

Modes:
    tty    stdin is the terminal          — someone running scripts/setup.sh
    pipe   stdin is a pipe, ctty present  — curl -fsSL … | bash   (the #341 case)
    none   stdin is a pipe, no ctty       — CI, launchd, a detached service

Prints the child's output; exits with the child's status.
"""
import fcntl
import os
import pty
import select
import sys
import termios


def run(mode, argv, feed=b""):
    master, slave = pty.openpty()

    # Echo off: input we inject for a prompt would otherwise come back in the
    # captured output and callers would assert against their own keystrokes.
    attrs = termios.tcgetattr(slave)
    attrs[3] &= ~termios.ECHO
    termios.tcsetattr(slave, termios.TCSANOW, attrs)

    r, w = os.pipe()
    pid = os.fork()
    if pid == 0:
        os.setsid()  # drop the inherited controlling terminal
        if mode != "none":
            # Claim the pty as this session's controlling terminal, so that
            # opening /dev/tty in the child resolves to it.
            fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
        os.dup2(slave if mode == "tty" else r, 0)
        os.dup2(slave, 1)
        os.dup2(slave, 2)
        for fd in (master, slave, r, w):
            os.close(fd)
        os.execv("/bin/bash", ["/bin/bash"] + argv)

    os.close(slave)
    os.close(r)
    os.close(w)  # the pipe is stdin only in 'pipe'/'none'; EOF immediately

    if feed:
        os.write(master, feed)

    out = b""
    while True:
        try:
            ready, _, _ = select.select([master], [], [], 10)
            if not ready:
                break
            chunk = os.read(master, 4096)
            if not chunk:
                break
            out += chunk
        except OSError:
            break  # slave closed — normal child exit

    _, status = os.waitpid(pid, 0)
    os.close(master)
    return out.decode(errors="replace"), (status >> 8) if status < 256 else 1


if __name__ == "__main__":
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    feed = sys.stdin.buffer.read() if not sys.stdin.isatty() else b""
    text, code = run(sys.argv[1], sys.argv[2:], feed)
    sys.stdout.write(text)
    sys.exit(code)
