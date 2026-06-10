package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/agentcreds"
)

// newAuthCmd groups agent-credential plumbing between client and daemon.
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage agent credentials on the daemon host.",
		Long: "Islands inherit Claude credentials from the daemon host. `dejima auth push` " +
			"sends this machine's Claude login (macOS Keychain or ~/.claude/.credentials.json) " +
			"to the daemon so islands authenticate without ever running a browser OAuth flow " +
			"on the daemon host. `dejima auth status` shows what new islands will get.",
	}
	cmd.AddCommand(newAuthPushCmd(), newAuthStatusCmd())
	return cmd
}

func newAuthPushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push",
		Short: "Send this machine's Claude credentials to the daemon.",
		RunE: func(cmd *cobra.Command, args []string) error {
			blob, source, err := agentcreds.LoadClaude()
			if err != nil {
				return err
			}
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.PushClaudeCredentials(cmd.Context(), blob); err != nil {
				return err
			}
			fmt.Printf("pushed Claude credentials (from %s) — new islands will use them\n", source)
			fmt.Println("note: existing islands keep their own copy; `dejima reset <name>` re-seeds one")
			return nil
		},
	}
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether new islands will get Claude credentials.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			st, err := c.ClaudeCredentialsStatus(cmd.Context())
			if err != nil {
				return err
			}
			switch st.HostSource {
			case "":
				fmt.Println("daemon host login: none (no Keychain entry or ~/.claude/.credentials.json)")
			default:
				fmt.Printf("daemon host login: yes (%s)\n", st.HostSource)
			}
			if st.SeedPresent {
				fmt.Printf("island seed:       present (updated %s)\n", st.SeedUpdatedAt.Local().Format(time.RFC1123))
			} else {
				fmt.Println("island seed:       missing")
			}
			if !st.SeedPresent && st.HostSource == "" {
				fmt.Println("\nnew islands will start WITHOUT Claude credentials.")
				fmt.Println("fix: run `dejima auth push` from a machine where `claude` is logged in.")
			}
			return nil
		},
	}
}
