package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// `dejima github host-credential` — the operator surface for the one credential
// that used to be inherited rather than granted: the host operator's own `gh`
// login. Its read scope is the whole account, so an island holding it can read
// every private repo that account can see. It is now deny-by-default like every
// other grant (Port, capability, MCP, link, spawn, egress), and this is how an
// operator turns it back on for an island that genuinely needs it.

func newGithubHostCredentialCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "host-credential",
		Aliases: []string{"host-cred"},
		Short:   "Grant/revoke an island the HOST operator's own GitHub login.",
		Long: "The host operator's `gh` login can read EVERY private repo on that account.\n" +
			"Islands don't get it by default; this grants it to one island explicitly.\n\n" +
			"Prefer a per-island identity where you can (`dejima github connect`, or\n" +
			"docs/github-identities.md) — that scopes the credential to what the island\n" +
			"actually needs, and takes precedence over this grant.",
	}
	cmd.AddCommand(newGithubHostCredentialStatusCmd(), newGithubHostCredentialGrantCmd(), newGithubHostCredentialRevokeCmd())
	return cmd
}

func newGithubHostCredentialStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <island>",
		Short: "Show whether an island may use the host operator's GitHub login.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			v, err := c.GetHostGitHubCredential(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			switch {
			case !v.Eligible:
				fmt.Printf("%s: not applicable — this island belongs to a tenant and uses its own GitHub identity.\n", args[0])
			case !v.Granted:
				fmt.Printf("%s: DENIED — no host GitHub credential (the default).\n", args[0])
			case v.Grandfathered:
				fmt.Printf("%s: GRANTED (grandfathered %s)\n", args[0], v.GrantedAt.Format("2006-01-02"))
				fmt.Println("  This grant was written by the migration to preserve the island's existing")
				fmt.Println("  access — nobody has decided about it yet. It reads the whole account.")
				fmt.Printf("  Close it with: dejima github host-credential revoke %s\n", args[0])
			default:
				by := v.GrantedBy
				if by == "" {
					by = "the host operator"
				}
				fmt.Printf("%s: GRANTED by %s on %s\n", args[0], by, v.GrantedAt.Format("2006-01-02"))
			}
			return nil
		},
	}
}

func newGithubHostCredentialGrantCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "grant <island>",
		Short: "Let an island use the host operator's own GitHub login.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if _, err := c.GrantHostGitHubCredential(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("granted: %s may use the host operator's GitHub login.\n", args[0])
			// The credential is a bind mount, so it is fixed at container create.
			// Saying this plainly avoids the "I granted it and nothing changed" loop.
			fmt.Printf("takes effect when the container is next created — `dejima upgrade %s` applies it now.\n", args[0])
			return nil
		},
	}
}

func newGithubHostCredentialRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <island>",
		Short: "Stop an island using the host operator's GitHub login.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RevokeHostGitHubCredential(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("revoked: %s no longer uses the host operator's GitHub login.\n", args[0])
			fmt.Printf("takes effect when the container is next created — `dejima upgrade %s` applies it now.\n", args[0])
			return nil
		},
	}
}
