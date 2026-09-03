package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"

	"github.com/aoos/dejima/internal/localmodel"
)

// installLocalBackendHere installs the inference backend on THIS machine, from
// the CLI, because the CLI is the process holding the operator's terminal.
//
// The daemon cannot do this on macOS. Ollama's installer copies its .app into
// /Applications and then SUDOs to link the CLI onto PATH; a LaunchDaemon has no
// controlling terminal, so that sudo dies on "a terminal is required to read the
// password" and the operator sees a completed 100% download followed by a bare
// `ERROR: exit status 1`. No daemon-side change can fix that — the password
// prompt needs a terminal, and only this process has one. So the install moves
// here, and the daemon keeps the half it can do: registering the `local`
// provider so islands can reach the backend.
//
// Reports whether the backend is present afterwards. A false return is not fatal
// — the caller falls through to the daemon path, which is correct on Linux and
// for a remote daemon.
func installLocalBackendHere(ctx context.Context) bool {
	if runtime.GOOS != "darwin" {
		return false // the official script is unattended on Linux; the daemon can run it
	}
	if resolveHost() != "" {
		// Pointed at a REMOTE daemon. The backend runs where the GPU and the
		// islands are, so installing it on this laptop would put a multi-GB model
		// on the wrong machine and still leave the host without one.
		return false
	}
	if installed, _ := localmodel.NewOllama().Detect(ctx); installed {
		return true
	}

	// Homebrew first: it needs no sudo at all, which removes the failure mode
	// this function exists for rather than just giving it a terminal.
	ensureBrewOnPath()
	if _, err := exec.LookPath("brew"); err == nil {
		fmt.Println("Installing Ollama with Homebrew (no password needed)…")
		if err := execInteractive("brew", "install", "ollama"); err == nil {
			// Start it now and across reboots. The daemon reaches the backend on
			// 127.0.0.1:11434, so one that is installed but not running reads as
			// "installed (not running)" and no island can use it.
			if err := execInteractive("brew", "services", "start", "ollama"); err != nil {
				fmt.Printf("⚠ couldn't start the ollama service (%v) — start it with: brew services start ollama\n", err)
			}
			installed, _ := localmodel.NewOllama().Detect(ctx)
			return installed
		}
		fmt.Println("Homebrew install didn't finish — falling back to Ollama's own installer.")
	}

	// No Homebrew, or it failed: the official script, run HERE so its sudo has a
	// terminal to prompt at. Same script the daemon was running; the terminal is
	// the entire difference.
	fmt.Println("Running Ollama's installer (it may ask for your password)…")
	if err := execInteractive("sh", "-c", "curl -fsSL https://ollama.com/install.sh | sh"); err != nil {
		fmt.Printf("✗ Ollama install didn't finish: %v\n", err)
		return false
	}
	installed, _ := localmodel.NewOllama().Detect(ctx)
	return installed
}
