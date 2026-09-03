package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// newGithubCmd is the self-serve GitHub identity group: a team member connects
// their OWN GitHub (for private-repo clones) without an operator pasting a PAT.
func newGithubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Connect your GitHub identity for private-repo clones (self-serve).",
	}
	cmd.AddCommand(newGithubConnectCmd(), newGithubHostCredentialCmd(),
		newGithubIdentityListCmd(), newGithubIdentityDefaultCmd(), newGithubIdentityRemoveCmd(),
		newGithubRepointCmd())
	return cmd
}

// newGithubConnectCmd drives the guided device-flow sign-in: start → show the
// user code + open github.com/login/device → poll until authorized, then the
// daemon stores the captured token as the caller's own tenant-scoped identity.
// No token is ever pasted or handled client-side. If the daemon has no OAuth app
// configured, start returns an error whose message points at the PAT path.
func newGithubConnectCmd() *cobra.Command {
	var makeDefault, shared, noOpen, tokenStdin bool
	var token string
	cmd := &cobra.Command{
		Use:   "connect [name]",
		Short: "Connect your GitHub via a guided sign-in (device flow — no token to paste).",
		Long: "Runs GitHub's device-flow: it shows a short code, you approve it at\n" +
			"github.com/login/device, and the daemon captures the token into your own\n" +
			"identity — nothing is pasted. [name] is the identity name to store it under\n" +
			"(default: \"github\").\n\n" +
			"If guided sign-in isn't configured on the daemon (the default for a self-hosted\n" +
			"install), this falls back to your local `gh` session automatically — same result.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			name := "github"
			if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
				name = strings.TrimSpace(args[0])
			}

			if tokenStdin {
				b, rerr := io.ReadAll(os.Stdin)
				if rerr != nil {
					return fmt.Errorf("read token from stdin: %w", rerr)
				}
				token = strings.TrimSpace(string(b))
			}
			// An explicitly supplied token means "use this", so don't start a
			// device flow the operator has already opted out of.
			if strings.TrimSpace(token) != "" {
				return connectGitHubViaToken(ctx, c, name, token, makeDefault, shared)
			}

			start, err := c.GitHubDeviceStart(ctx)
			if err != nil {
				// A self-hosted daemon has no OAuth app, so guided sign-in is dark
				// by default. Don't dead-end on the command the operator was just
				// told to run — complete the job over the token path instead.
				if deviceFlowUnconfigured(err) {
					return connectGitHubViaToken(ctx, c, name, token, makeDefault, shared)
				}
				return err
			}

			fmt.Println()
			fmt.Println(bold("Connect your GitHub"))
			fmt.Println()
			fmt.Printf("  1. Open   %s\n", start.VerificationURI)
			fmt.Printf("  2. Enter code   %s\n", start.UserCode)
			if start.Scopes != "" {
				fmt.Printf("     (grants Dejima: %s)\n", start.Scopes)
			}
			fmt.Println()
			if !noOpen {
				_ = openURL(start.VerificationURI) // best-effort; the URL is printed above regardless
			}
			fmt.Println("  Waiting for you to authorize on GitHub… (Ctrl-C to cancel)")

			interval := start.Interval
			if interval <= 0 {
				interval = 5
			}
			deadline := time.Now().Add(time.Duration(max(start.ExpiresIn, 60)) * time.Second)
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(interval) * time.Second):
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("the code expired before you authorized — run `dejima github connect` again")
				}
				resp, err := c.GitHubDevicePoll(ctx, api.GitHubDevicePollRequest{
					SessionID: start.SessionID, Name: name, Default: makeDefault, Shared: shared,
				})
				if err != nil {
					return err
				}
				if resp.Interval > 0 {
					interval = resp.Interval // honor the server's backoff (it bumps on slow_down)
				}
				switch resp.State {
				case "authorization_pending", "slow_down":
					continue
				case "authorized":
					fmt.Println()
					fmt.Printf("  ✓ connected as %s (identity %q)\n", resp.Login, resp.Identity)
					fmt.Println("    New islands cloning your private repos will use it.")
					return nil
				case "access_denied":
					return fmt.Errorf("authorization was denied on GitHub")
				case "expired":
					return fmt.Errorf("the code expired — run `dejima github connect` again")
				default:
					return fmt.Errorf("unexpected device-flow state %q", resp.State)
				}
			}
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "connect with this GitHub token instead of a guided sign-in (for daemons with no OAuth app and no gh CLI)")
	cmd.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the GitHub token from stdin (keeps it out of your shell history and the process list)")
	cmd.Flags().BoolVar(&makeDefault, "default", false, "make this the daemon's default identity (host owner only)")
	cmd.Flags().BoolVar(&shared, "shared", false, "let every tenant's islands use this identity (host owner only)")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "don't auto-open the browser (just print the URL + code)")
	return cmd
}
