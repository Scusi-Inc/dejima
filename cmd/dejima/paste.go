package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/pasteimg"
)

// apiClient is the slice of *api.Client resolveAgentTmux needs — narrowed to an
// interface so the agent-resolution logic is unit-testable with a fake.
type apiClient interface {
	ListAgents(ctx context.Context, name string) ([]api.AgentInfo, error)
}

// pasteIntakeDir is the in-island directory clipboard images land in. It reuses
// the Port intake convention (/home/dejima/intake/<scope>/…, see
// internal/api/port_trade.go) — same writable, agent-owned tree, under a
// dedicated "paste" scope so clipboard drops are distinguishable from brokered
// host-file intakes. /home/dejima is writable by the island's dejima user; a
// top-level /intake is not (the container root is root-owned).
const pasteIntakeDir = "/home/dejima/intake/paste"

// newPasteCmd is the host→island image-paste bridge: capture an image off the
// HOST clipboard, stage it into the target island's intake dir, then inject a
// `Read <path>` line into the agent's prompt so it picks the image up.
//
// Capture is host-platform-specific (macOS clipboard today) and degrades
// gracefully where unsupported — the bridge is a convenience, never required.
func newPasteCmd() *cobra.Command {
	var agentID string
	var noInject bool
	cmd := &cobra.Command{
		Use:   "paste <island>[/<agent>]",
		Short: "Paste the host clipboard image into an island and point the agent at it.",
		Long: "Capture an image from the HOST system clipboard, write it into the island's " +
			"intake dir, and inject a `Read <path>` line into the target agent's prompt so it " +
			"reads the image.\n\n" +
			"Clipboard-image capture is host-platform-specific (macOS today). Where it isn't " +
			"supported, save the image to a file and use `dejima cp ./img.png <island>:" +
			pasteIntakeDir + "/img.png` instead.\n\n" +
			"  dejima paste myrepo            # into the island's first interactive agent\n" +
			"  dejima paste myrepo/reviewer   # into a specific agent",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, agent := splitIslandAgent(args[0])
			if agentID != "" {
				agent = agentID
			}

			img, err := pasteimg.Capture()
			if err != nil {
				return pasteCaptureHint(err)
			}

			c, err := client()
			if err != nil {
				return err
			}

			// Resolve the target agent (and its tmux session) so we know where to
			// inject. Default to the first attachable agent (the primary prompt).
			tmux, resolvedAgent, err := resolveAgentTmux(cmd, c, island, agent)
			if err != nil {
				return err
			}

			dest := fmt.Sprintf("%s/clip-%s.%s", pasteIntakeDir, time.Now().Format("20060102-150405"), img.Ext)
			if err := c.WriteFile(cmd.Context(), island, dest, bytes.NewReader(img.Bytes)); err != nil {
				return fmt.Errorf("stage image into island: %w", err)
			}
			fmt.Printf("pasted %d bytes → %s:%s\n", len(img.Bytes), island, dest)

			if noInject || tmux == "" {
				if tmux == "" && !noInject {
					fmt.Printf("agent %q has no interactive session; not injecting. Tell it: Read %s\n", resolvedAgent, dest)
				}
				return nil
			}

			// Inject via the same `tmux send-keys` the wake path uses, run through
			// the existing exec channel — no new endpoint, no grant routes touched.
			line := "Read " + dest
			res, err := c.ExecInIsland(cmd.Context(), island, []string{"tmux", "send-keys", "-t", tmux, line, "Enter"})
			if err != nil {
				return fmt.Errorf("inject `Read` into agent %q: %w", resolvedAgent, err)
			}
			if res != nil && res.ExitCode != 0 {
				return fmt.Errorf("inject into agent %q: tmux send-keys exit %d", resolvedAgent, res.ExitCode)
			}
			fmt.Printf("injected into %s/%s: %s\n", island, resolvedAgent, line)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "", "target agent id (default: the island's first interactive agent)")
	cmd.Flags().BoolVar(&noInject, "no-inject", false, "stage the image but don't inject a `Read` line")
	return cmd
}

// resolveAgentTmux returns the tmux session + resolved id of the target agent.
// When agent is empty it picks the first attachable (interactive) agent — the
// one with a prompt to inject into.
func resolveAgentTmux(cmd *cobra.Command, c apiClient, island, agent string) (tmux, id string, err error) {
	agents, err := c.ListAgents(cmd.Context(), island)
	if err != nil {
		return "", "", err
	}
	if len(agents) == 0 {
		return "", "", fmt.Errorf("island %q has no agents", island)
	}
	if agent != "" {
		for _, a := range agents {
			if a.ID == agent {
				return a.Tmux, a.ID, nil
			}
		}
		return "", "", fmt.Errorf("island %q has no agent %q", island, agent)
	}
	// No agent named: prefer the first attachable (interactive) agent.
	for _, a := range agents {
		if a.Attachable && a.Tmux != "" {
			return a.Tmux, a.ID, nil
		}
	}
	// Fall back to the first agent so staging still happens even if none is
	// attachable (injection is then skipped by the caller on an empty tmux).
	return agents[0].Tmux, agents[0].ID, nil
}

// pasteCaptureHint turns a capture failure into actionable advice. Unsupported
// platforms and an empty clipboard are user conditions, not errors to dump.
func pasteCaptureHint(err error) error {
	switch {
	case errors.Is(err, pasteimg.ErrUnsupported):
		return fmt.Errorf("clipboard image capture isn't supported on this host yet; " +
			"save the image and use `dejima cp ./img.png <island>:" + pasteIntakeDir + "/img.png`")
	case errors.Is(err, pasteimg.ErrNoImage):
		return errors.New("no image on the clipboard — copy an image first, then `dejima paste`")
	default:
		return err
	}
}
