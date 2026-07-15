package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/voicein"
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
		Use:   "voice <island>[/<agent>]",
		Short: "Dictate into an island's agent with your voice (local, on-device transcription).",
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
				return fmt.Errorf("voice dictation isn't supported on this host platform yet")
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
	cmd.AddCommand(newVoiceInstallCmd(), newVoiceStatusCmd())
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
				return fmt.Errorf("voice dictation isn't supported on this host platform yet")
			}
			st := voicein.Check()
			plan := voicein.PlanInstall(st)
			if plan.Empty() {
				fmt.Println("Voice dictation is already set up. ✓")
				return nil
			}
			if err := voicein.Install(cmd.Context(), plan, os.Stdout); err != nil {
				return err
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
				fmt.Println("Voice dictation: unsupported on this host platform")
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
