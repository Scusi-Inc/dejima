package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// newTokenCmd is the team-auth token surface: issue, list, and revoke the
// operator bearer tokens that carry the role/scope model. These complement the
// tailnet identity — a request with no token is still the trusted caller; a token
// hands a *narrower* credential (a role, optionally scoped to specific islands)
// to an automated client without sharing the operator's own access.
func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Issue, list, and revoke operator API tokens (owner/operator/viewer + island scope).",
		Long: "Mint attenuated bearer tokens for the operator API. Three roles:\n" +
			"  owner     full authority (purge, credentials, token admin)\n" +
			"  operator  island lifecycle — create/wake/exec/attach — but NOT purge\n" +
			"  viewer    read + observe only (list, status, logs, audit)\n\n" +
			"A token may be scoped to specific islands with --island (repeatable).\n" +
			"Consumers present it as `Authorization: Bearer <secret>`; the CLI picks it\n" +
			"up from DEJIMA_TOKEN. The secret is shown once at creation and never stored\n" +
			"in the clear — only its hash is kept, so a lost secret means minting anew.",
	}
	cmd.AddCommand(newTokenCreateCmd(), newTokenLsCmd(), newTokenRevokeCmd())
	return cmd
}

func newTokenCreateCmd() *cobra.Command {
	var role, label string
	var islands []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Mint a new token and print its secret (shown once).",
		RunE: func(cmd *cobra.Command, args []string) error {
			role = strings.TrimSpace(role)
			if role == "" {
				return fmt.Errorf("--role is required (owner, operator, or viewer)")
			}
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.CreateToken(cmd.Context(), api.CreateTokenRequest{
				Label:   label,
				Role:    role,
				Islands: islands,
			})
			if err != nil {
				return err
			}
			scope := "all islands"
			if len(resp.Token.Islands) > 0 {
				scope = strings.Join(resp.Token.Islands, ", ")
			}
			fmt.Printf("created token %s (role: %s; scope: %s)\n", resp.Token.ID, resp.Token.Role, scope)
			fmt.Println()
			fmt.Println("  bearer secret (shown once — store it now):")
			fmt.Printf("    %s\n", resp.Secret)
			fmt.Println()
			fmt.Println("  use it from a client:")
			fmt.Println("    export DEJIMA_TOKEN=" + resp.Secret)
			fmt.Println("    export DEJIMA_HOST=<daemon-host:port>")
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "owner, operator, or viewer (required)")
	cmd.Flags().StringVar(&label, "label", "", "human label for the token (e.g. scusi-prod, phone)")
	cmd.Flags().StringArrayVar(&islands, "island", nil, "limit the token to this island (repeatable); default: all islands")
	return cmd
}

func newTokenLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List issued tokens (metadata only — never the secret).",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			toks, err := c.ListTokens(cmd.Context())
			if err != nil {
				return err
			}
			if len(toks) == 0 {
				fmt.Println("no tokens issued — `dejima token create --role <role>` to mint one")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tROLE\tLABEL\tSCOPE\tCREATED")
			for _, t := range toks {
				scope := "all"
				if len(t.Islands) > 0 {
					scope = strings.Join(t.Islands, ",")
				}
				label := t.Label
				if label == "" {
					label = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					t.ID, t.Role, label, scope, t.CreatedAt.Local().Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}
}

func newTokenRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a token by id.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RevokeToken(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("revoked token %s\n", args[0])
			return nil
		},
	}
}
