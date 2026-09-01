package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newDoctorTmuxSizeCmd captures the tmux sizing state of an island's sessions.
//
// It exists because "my terminal went black" was diagnosed wrong three times
// from reasoning alone. The distinguishing fact — is the WINDOW smaller than the
// CLIENT attached to it? — is one `tmux list-clients` away and was never asked,
// so each round produced a plausible cause and no evidence.
//
// Run it from a SECOND terminal while a pane is black. A window narrower than
// its client is the collapse (a sizeless attach becoming the "latest" client
// under `window-size latest`). Matching sizes rule that out and point at the
// process inside the pane instead.
func newDoctorTmuxSizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tmux-size <island>",
		Short: "Show tmux client/window sizes for an island — run this WHILE a pane is black.",
		Long: "Prints, per tmux session in the island: every attached client with its size\n" +
			"and TERM, the window and pane dimensions, and the sizing options in force.\n\n" +
			"What to look for: a WINDOW smaller than the CLIENT attached to it. That is a\n" +
			"collapsed window — a sizeless attach became the `latest` client and shrank the\n" +
			"session for everyone. If the sizes agree, the window is fine and the blank pane\n" +
			"is the process inside it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			c, err := client()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			out := cmd.OutOrStdout()

			// One invocation per fact, so a failure in one still leaves the others
			// readable. A half-captured report beats a hard error when the operator
			// is staring at a black screen and cannot easily retry.
			for _, pr := range tmuxSizeProbes {
				fmt.Fprintf(out, "\n== %s ==\n", pr.label)
				res, err := c.ExecInIsland(ctx, island, []string{"bash", "-lc", pr.script})
				switch {
				case err != nil:
					fmt.Fprintf(out, "  (could not run: %v)\n", err)
				case res.ExitCode != 0:
					fmt.Fprintf(out, "  (exited %d) %s\n", res.ExitCode, strings.TrimSpace(res.Stderr))
				default:
					body := strings.TrimSpace(res.Stdout)
					if body == "" {
						body = "(no output)"
					}
					for _, line := range strings.Split(body, "\n") {
						fmt.Fprintf(out, "  %s\n", line)
					}
				}
			}
			fmt.Fprintln(out, "\nRead it this way: compare each WINDOW's size to the CLIENT attached to")
			fmt.Fprintln(out, "that session. A window smaller than its client is a collapsed window.")
			fmt.Fprintln(out, "Capture this verbatim — a summary drops the one field that decides it.")
			return nil
		},
	}
}

// tmuxSizeProbes are the facts worth having when a pane is blank. Client size and
// window size are listed separately and adjacently ON PURPOSE: the diagnosis is
// the COMPARISON between them, and a report that folded them together would hide
// exactly the discrepancy it is being run to find.
var tmuxSizeProbes = []struct{ label, script string }{
	{"sessions", `tmux list-sessions -F '#{session_name}  windows=#{session_windows}  attached=#{session_attached}'`},
	{"clients (size + TERM)", `tmux list-clients -F '#{client_tty}  #{client_width}x#{client_height}  TERM=#{client_termname}  session=#{client_session}'`},
	{"windows", `tmux list-windows -a -F '#{session_name}:#{window_index}  #{window_width}x#{window_height}  zoomed=#{window_zoomed_flag}'`},
	{"panes", `tmux list-panes -a -F '#{session_name}:#{window_index}.#{pane_index}  #{pane_width}x#{pane_height}  dead=#{pane_dead}  cmd=#{pane_current_command}'`},
	{"sizing options", `tmux show -g window-size; tmux show -g default-size; tmux show -g aggressive-resize`},
}
