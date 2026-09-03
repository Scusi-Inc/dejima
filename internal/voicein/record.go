package voicein

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// stopGrace is how long a recorder gets to finalize its WAV after we ask it to
// stop (SIGINT) before we force-kill it.
const stopGrace = 2 * time.Second

// Record captures the host microphone to a 16 kHz mono WAV at wav, recording
// until ctx is cancelled — the push-to-talk model: the caller cancels ctx when
// the user ends the utterance (e.g. presses Enter / releases the key). It stops
// the recorder with SIGINT first so the tool writes a valid WAV header, only
// force-killing if it doesn't exit within stopGrace.
//
// ErrUnsupported on a platform with no recorder path; ErrNotInstalled when a
// supported platform simply lacks the tool (install provisions sox `rec`).
func Record(ctx context.Context, wav string) error {
	tool := firstOnPath(recorders())
	if tool == "" {
		if !Supported() {
			return ErrUnsupported
		}
		return ErrNotInstalled
	}
	// Windows needs a named capture device; elsewhere this is "" and ignored.
	device, derr := resolveDevice(ctx)
	if derr != nil {
		return derr
	}
	cmd := command(tool, recorderArgsFor(tool, wav, device)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start recorder %q: %w", tool, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		// Ask the recorder to stop and finalize the WAV; force-kill if it hangs.
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(stopGrace):
			_ = cmd.Process.Kill()
			<-done
		}
	case err := <-done:
		// The recorder exited on its own before we asked — a real failure (no mic,
		// permission denied, device busy), since a healthy capture runs until we
		// stop it.
		if err != nil {
			return fmt.Errorf("recorder %q exited: %v: %s", tool, err, strings.TrimSpace(stderr.String()))
		}
	}

	// A recorder stopped via SIGINT typically exits non-zero, but the WAV is
	// valid — so we judge success by the file, not the exit code. An empty/absent
	// file means nothing was captured (most often an ungranted mic permission).
	if fi, err := os.Stat(wav); err != nil || fi.Size() == 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = "check the host's microphone permission for your terminal"
		}
		return fmt.Errorf("recording produced no audio — %s", msg)
	}
	return nil
}
