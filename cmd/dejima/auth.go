package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/agentcreds"
	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/reposrc"
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
	var github bool
	var name string
	var makeDefault bool
	var host string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Send this machine's credentials to the daemon.",
		Long: "Without flags, pushes this machine's Claude login. With --github, pushes the " +
			"active gh account as a named daemon GitHub identity that islands can clone and push " +
			"as — switch accounts with `gh auth switch` first to seed the other one.",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if github {
				login, token, err := reposrc.LocalGitHubLogin(host)
				if err != nil {
					return err
				}
				// Verify before storing so a bad/expired token fails here, not
				// later inside an island. The verified login wins if gh and the
				// token disagree (e.g. a token minted for another account).
				verified, ghUserID, err := githubid.VerifyToken(cmd.Context(), host, token)
				if err != nil {
					return fmt.Errorf("token verification failed (nothing stored): %w", err)
				}
				if verified != "" {
					login = verified
				}
				id := name
				if id == "" {
					id = login
				}
				if _, err := c.PutGitHubIdentity(cmd.Context(), id, api.PutGitHubIdentityRequest{
					Login: login, ID: ghUserID, Host: host, Token: token, Default: makeDefault,
				}); err != nil {
					return err
				}
				where := "github.com"
				if h := strings.TrimSpace(host); h != "" {
					where = h
				}
				fmt.Printf("pushed GitHub identity %q (login %s @ %s) to the daemon\n", id, login, where)
				fmt.Println("new islands can select it; see `dejima auth status`")
				return nil
			}
			blob, source, err := agentcreds.LoadClaude()
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
	cmd.Flags().BoolVar(&github, "github", false, "push the active gh account as a daemon GitHub identity")
	cmd.Flags().StringVar(&name, "name", "", "identity name (default: the GitHub login)")
	cmd.Flags().BoolVar(&makeDefault, "default", false, "make this the daemon's default GitHub identity")
	cmd.Flags().StringVar(&host, "host", "", "GitHub host for --github (default github.com; set a GitHub Enterprise host to seed an enterprise identity)")
	return cmd
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

			ids, err := c.ListGitHubIdentities(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Println()
			if len(ids) == 0 {
				fmt.Println("github identities: none")
				fmt.Println("  add one with `dejima auth push --github` (from a machine with gh),")
				fmt.Println("  or `gh auth login` on the daemon host. Islands fall back to the host's ~/.config/gh.")
			} else {
				fmt.Println("github identities:")
				for _, id := range ids {
					marker := " "
					if id.Default {
						marker = "*"
					}
					fmt.Printf("  %s %-12s %s@%s\n", marker, id.Name, id.Login, id.Host)
				}
				fmt.Println("  (* = default; new islands use it unless they pick another)")
			}
			return nil
		},
	}
}
