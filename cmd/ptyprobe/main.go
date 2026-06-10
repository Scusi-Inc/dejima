// Command ptyprobe isolates the daemon's tmux attach + resize path with no
// websocket/daemon involved. It reproduces exactly what internal/bridge does:
//
//  1. create a DETACHED tmux session (like image/start.sh does at boot, which
//     comes up at default-size, NOT the client size)
//  2. `pty.StartWithSize(69x138)` running `tmux attach` against it (like the
//     daemon's `new-session -A` when the session already exists)
//  3. read back what tmux thinks the CLIENT size is (via a separate
//     `tmux list-clients`) — this is what drives `window-size latest`
//  4. live-resize the PTY to 69x100 and read the client size again
//
// Run on the HOST:
//
//	go run ./cmd/ptyprobe                 # container dejima-dejima
//	go run ./cmd/ptyprobe dejima-other
//
// If list-clients shows 69x138 then 69x100, the tmux layer tracks the PTY and
// the bug is in the daemon handshake (active connection on the 0x0 path). If it
// stays 80x24 / shows the wrong size, tmux is reading the client size before
// docker applies it (an attach-time race) and never catching the SIGWINCH.
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
)

const probeSession = "probetest"

func docker(container string, args ...string) string {
	full := append([]string{"exec", container}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(err: %v) %s", err, bytes.TrimSpace(out))
	}
	return string(bytes.TrimSpace(out))
}

func main() {
	container := "dejima-dejima"
	if len(os.Args) > 1 {
		container = os.Args[1]
	}

	// Clean slate, then create a DETACHED session — same shape as boot.
	_ = exec.Command("docker", "exec", container, "tmux", "kill-session", "-t", probeSession).Run()
	fmt.Println("create detached:", docker(container, "tmux", "new-session", "-d", "-s", probeSession))
	fmt.Println("window size after detached create:",
		docker(container, "tmux", "display-message", "-p", "-t", probeSession, "#{window_width}x#{window_height}"))

	// Attach via a 69x138 PTY, exactly like the daemon's StartWithSize path.
	cmd := exec.Command("docker", "exec", "-it", container,
		"tmux", "attach-session", "-t", probeSession)
	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 69, Cols: 138})
	if err != nil {
		fmt.Fprintln(os.Stderr, "StartWithSize:", err)
		os.Exit(1)
	}
	defer func() {
		_ = ptyFile.Close()
		_ = exec.Command("docker", "exec", container, "tmux", "kill-session", "-t", probeSession).Run()
	}()
	go io.Copy(io.Discard, ptyFile) // drain output so tmux isn't blocked

	time.Sleep(1500 * time.Millisecond)
	fmt.Println("\n--- after attach at 69x138 ---")
	fmt.Println("list-clients:", docker(container, "tmux", "list-clients", "-t", probeSession))
	fmt.Println("window size:",
		docker(container, "tmux", "display-message", "-p", "-t", probeSession, "#{window_width}x#{window_height}"))

	fmt.Println("\n>>> live resize PTY -> 69x100")
	if err := pty.Setsize(ptyFile, &pty.Winsize{Rows: 69, Cols: 100}); err != nil {
		fmt.Fprintln(os.Stderr, "Setsize:", err)
	}
	time.Sleep(1500 * time.Millisecond)
	fmt.Println("--- after live resize to 69x100 ---")
	fmt.Println("list-clients:", docker(container, "tmux", "list-clients", "-t", probeSession))
	fmt.Println("window size:",
		docker(container, "tmux", "display-message", "-p", "-t", probeSession, "#{window_width}x#{window_height}"))
	fmt.Println("\n>>> done")
}
