// Command ptyprobe isolates the daemon's resize path: it starts `docker exec
// -it` against a host PTY using creack/pty (exactly like internal/bridge does),
// sizes it to 69x138, then does a live resize to 69x100. Inside the container
// it loops `stty size` so we can watch whether the size actually propagates.
//
// Run on the HOST (it needs the host's docker):
//
//	go run ./cmd/ptyprobe                 # defaults to container dejima-dejima
//	go run ./cmd/ptyprobe dejima-other    # override container
//
// Expected if propagation works: stty prints "69 138", then "69 100" after the
// live resize. If it prints "24 80" throughout, docker isn't taking the size
// from the creack/pty PTY — that's the bug.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
)

func main() {
	container := "dejima-dejima"
	if len(os.Args) > 1 {
		container = os.Args[1]
	}

	cmd := exec.Command("docker", "exec", "-it", container,
		"sh", "-c", "while true; do stty size; sleep 0.5; done")

	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 69, Cols: 138})
	if err != nil {
		fmt.Fprintln(os.Stderr, "StartWithSize:", err)
		os.Exit(1)
	}
	defer func() { _ = ptyFile.Close() }()

	// Pump container output (the stty size lines) to our stdout, tagged.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptyFile.Read(buf)
			if n > 0 {
				fmt.Printf("[stty] %s", buf[:n])
			}
			if err != nil {
				if err != io.EOF {
					fmt.Fprintln(os.Stderr, "read:", err)
				}
				return
			}
		}
	}()

	fmt.Println(">>> started at 69x138; watching for ~2s")
	time.Sleep(2 * time.Second)

	fmt.Println(">>> live resize -> 69x100 (pty.Setsize)")
	if err := pty.Setsize(ptyFile, &pty.Winsize{Rows: 69, Cols: 100}); err != nil {
		fmt.Fprintln(os.Stderr, "Setsize:", err)
	}
	time.Sleep(3 * time.Second)

	fmt.Println(">>> done")
}
