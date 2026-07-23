package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/voicein"
	"runtime"
)

// newVoiceCmd is the host→island voice-dictation bridge: capture the HOST
// microphone, transcribe it LOCALLY (whisper.cpp — no cloud, no subscription,
// audio never leaves the host), then inject the transcript into the target
// agent's prompt as a bracketed paste — the same injection path `dejima paste`
// uses for images. Only the transcribed text ever crosses into the island, so
// the container stays audio-deny-all.
//
//	dejima voice install        # one-time: whisper.cpp + sox + the base.en model
//	dejima voice status         # is the toolchain ready?
//	dejima voice myrepo         # push-to-talk → dictate into the island's agent
func newVoiceCmd() *cobra.Command {
	var agentID string
	var noInject bool

	cmd := &cobra.Command{
		Use: "voice <island>[/<agent>]",
		// Hidden: voice dictation is roadmapped, not shipped — the Windows
		// install path isn't automated and there's no in-session hotkey yet (see
		// docs/roadmap.md). The engine (internal/voicein) and these commands are
		// kept intact and callable so the rebuild starts from working code, but
		// they're off the help/completion surface so operators don't hit a
		// half-wired flow.
		Hidden: true,
		Short:  "Dictate into an island's agent with your voice (local, on-device transcription).",
		Long: "Capture the HOST microphone, transcribe it locally with whisper.cpp (no cloud, no " +
			"subscription — audio never leaves this machine), and inject the transcript into the " +
			"target agent's prompt. Push-to-talk: run it, speak, press Enter to stop.\n\n" +
			"Run `dejima voice install` once to set up the toolchain (whisper.cpp + a recorder + " +
			"the speech model). `dejima voice status` reports readiness.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, agent := splitIslandAgent(args[0])
			if agentID != "" {
				agent = agentID
			}

			st := voicein.Check()
			if !st.Supported {
				return errUnsupportedPlatform()
			}
			if !st.Ready() {
				return fmt.Errorf("voice dictation isn't set up (missing: %s) — run `dejima voice install`",
					joinAnd(st.Missing()))
			}

			c, err := client()
			if err != nil {
				return err
			}
			// Resolve the target agent + its tmux session up front, so we fail fast
			// before recording if there's nothing to inject into.
			tmux, resolvedAgent, err := resolveAgentTmux(cmd, c, island, agent)
			if err != nil {
				return err
			}
			if tmux == "" && !noInject {
				return fmt.Errorf("agent %q has no interactive session to dictate into", resolvedAgent)
			}

			text, err := dictate(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Printf("heard: %s\n", text)

			if noInject {
				return nil
			}
			// Inject as a bracketed paste (no Enter) so the transcript lands in the
			// agent's prompt for review/edit before it submits — same seam as
			// `dejima paste`.
			res, err := c.ExecInIsland(cmd.Context(), island,
				[]string{"tmux", "send-keys", "-t", tmux, "-l", bracketedPaste(text)})
			if err != nil {
				return fmt.Errorf("inject dictation into agent %q: %w", resolvedAgent, err)
			}
			if res != nil && res.ExitCode != 0 {
				return fmt.Errorf("inject into agent %q: tmux send-keys exit %d", resolvedAgent, res.ExitCode)
			}
			fmt.Printf("dictated → %s/%s\n", island, resolvedAgent)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "target agent id (default: the island's first interactive agent)")
	cmd.Flags().BoolVar(&noInject, "no-inject", false, "transcribe and print, but don't inject into the agent's prompt")
	cmd.AddCommand(newVoiceInstallCmd(), newVoiceStatusCmd(), newVoiceDeviceCmd())
	return cmd
}

// dictate runs one push-to-talk cycle: start recording, wait for the user to
// press Enter, stop, then transcribe locally. Returns the recognized text.
func dictate(ctx context.Context) (string, error) {
	wav, err := os.CreateTemp("", "dejima-voice-*.wav")
	if err != nil {
		return "", err
	}
	path := wav.Name()
	_ = wav.Close()
	defer os.Remove(path)

	recCtx, stop := context.WithCancel(ctx)
	recErr := make(chan error, 1)
	go func() { recErr <- voicein.Record(recCtx, path) }()

	fmt.Println("🎙  Recording… press Enter to stop.")
	waitForEnter()
	stop() // ask the recorder to finalize the WAV

	if err := <-recErr; err != nil {
		return "", err
	}
	fmt.Println("… transcribing locally.")
	return voicein.Transcribe(ctx, path)
}

// waitForEnter blocks until the user presses Enter (or stdin closes).
func waitForEnter() {
	r := bufio.NewReader(os.Stdin)
	_, _ = r.ReadString('\n')
}

// newVoiceInstallCmd provisions the local dictation toolchain: whisper.cpp + a
// recorder (sox) via Homebrew, and the speech model. Idempotent — only the
// missing pieces are installed.
func newVoiceInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the local voice-dictation toolchain (whisper.cpp + recorder + model).",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !voicein.Supported() {
				return errUnsupportedPlatform()
			}
			st := voicein.Check()
			plan := voicein.PlanInstall(st)
			manual := voicein.ManualSteps(st)
			if plan.Empty() && len(manual) == 0 {
				fmt.Println("Voice dictation is already set up. ✓")
				return nil
			}
			if !plan.Empty() {
				if err := voicein.Install(cmd.Context(), plan, os.Stdout); err != nil {
					return err
				}
			}
			// Platforms with no package-manager path get told exactly what to
			// install, rather than a refusal — the model (the large, fiddly
			// part) is already downloaded above.
			if len(manual) > 0 {
				fmt.Println()
				fmt.Println("Two tools still need installing by hand on this platform:")
				for i, step := range manual {
					fmt.Printf("  %d. %s\n", i+1, step)
				}
				fmt.Println()
				fmt.Println("Then re-run `dejima voice install` to confirm, and `dejima voice device` to pick a mic.")
				return nil
			}
			if voicein.Check().Ready() {
				fmt.Println("\n✓ Voice dictation ready — try: dejima voice <island>")
			}
			return nil
		},
	}
}

// newVoiceStatusCmd reports toolchain readiness — the seam the TUI tip/settings
// read to decide whether to nudge the user to set voice up.
func newVoiceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether local voice dictation is set up.",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			st := voicein.Check()
			if st.Ready() {
				fmt.Printf("Voice dictation: ready ✓\n  recorder: %s\n  whisper:  %s\n  model:    %s\n",
					st.Recorder, st.WhisperBin, filepath.Base(st.ModelPath))
				return nil
			}
			if !st.Supported {
				fmt.Printf("Voice dictation: not available on %s yet\n", runtime.GOOS)
				fmt.Println("  Voice records the microphone of the machine running this CLI (not the daemon host).")
				return nil
			}
			fmt.Printf("Voice dictation: not set up\n  missing: %s\n  run: dejima voice install\n",
				joinAnd(st.Missing()))
			return nil
		},
	}
}

// joinAnd renders a list as "a, b and c" for a friendly missing-components line.
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	head, tail := items[:len(items)-1], items[len(items)-1]
	out := ""
	for i, h := range head {
		if i > 0 {
			out += ", "
		}
		out += h
	}
	return out + " and " + tail
}

// errUnsupportedPlatform explains where voice dictation runs, instead of a bare
// "not supported". Voice captures the microphone of the machine running this
// CLI — not the daemon host — so an operator driving a remote daemon needs to
// set it up locally. The old message said none of that and read as a dead end.
func errUnsupportedPlatform() error {
	return fmt.Errorf("voice dictation isn't available on %s yet.\n\n"+
		"  Voice runs on the machine with the microphone — this one — and transcribes locally;\n"+
		"  audio never leaves it. Only the finished transcript is sent to the island's agent,\n"+
		"  so it works fine against a remote daemon.\n\n"+
		"  Supported: macOS, Linux, Windows.", runtime.GOOS)
}

// newVoiceDeviceCmd lists the microphones this host can capture from and saves
// the chosen one. Only meaningful where capture needs a named device (Windows
// dshow); elsewhere the system default is used and this reports that.
func newVoiceDeviceCmd() *cobra.Command {
	var pick string
	cmd := &cobra.Command{
		Use:   "device",
		Short: "Choose which microphone voice dictation records from.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			devices, err := voicein.ListDevices(cmd.Context())
			if err != nil {
				return err
			}
			if len(devices) == 0 {
				fmt.Println("This platform records from the system default microphone — nothing to choose.")
				fmt.Println("Change it in your OS sound settings.")
				return nil
			}
			if pick != "" {
				if err := voicein.SaveDevice(pick); err != nil {
					return err
				}
				fmt.Printf("voice dictation will record from %q\n", pick)
				return nil
			}
			current := voicein.SavedDevice()
			fmt.Println("Microphones available:")
			for i, d := range devices {
				marker := " "
				if d == current {
					marker = "*"
				}
				fmt.Printf("  %s %d) %s\n", marker, i+1, d)
			}
			fmt.Println()
			if current == "" {
				fmt.Printf("None chosen yet — %q would be used.\n", devices[0])
			}
			fmt.Println("Choose one with:  dejima voice device --set \"<name>\"")
			return nil
		},
	}
	cmd.Flags().StringVar(&pick, "set", "", "save this microphone as the capture device (exact name from the list)")
	return cmd
}
