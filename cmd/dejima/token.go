package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/clientcfg"
	"github.com/aoos/dejima/internal/invite"
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
	cmd.AddCommand(newTokenCreateCmd(), newTokenInviteCmd(), newTokenLsCmd(), newTokenRevokeCmd())
	return cmd
}

func newTokenCreateCmd() *cobra.Command {
	var role, label, owner string
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
				Owner:   owner,
				Islands: islands,
			})
			if err != nil {
				return err
			}
			scope := "all islands"
			if len(resp.Token.Islands) > 0 {
				scope = strings.Join(resp.Token.Islands, ", ")
			}
			tenant := resp.Token.Owner
			if tenant == "" {
				tenant = "host owner"
			}
			fmt.Printf("created token %s (role: %s; owner: %s; scope: %s)\n", resp.Token.ID, resp.Token.Role, tenant, scope)
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
	cmd.Flags().StringVar(&owner, "owner", "", "tenant this token acts as — an operator/viewer token then sees + creates only that tenant's islands (default: the host owner, i.e. full access)")
	cmd.Flags().StringArrayVar(&islands, "island", nil, "limit the token to this island (repeatable); default: all the owner's islands")
	return cmd
}

// newTokenInviteCmd mints a token and emits a single paste-safe invite blob
// (host + secret + role/scope) for a teammate to `dejima join`. It's the
// CLI twin of the TUI Team view's "invite" action — both call invite.Encode at
// mint time (the secret is only returned once).
func newTokenInviteCmd() *cobra.Command {
	var role, label, host, name, owner string
	var islands []string
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Mint a token and print a paste-safe invite blob for a teammate to `dejima join`.",
		Long: "Issue a team invite: mints a bearer token (owner/operator/viewer + optional island\n" +
			"scope) and bundles it with the daemon host into ONE paste-safe line. The teammate\n" +
			"runs `dejima join <blob>` to connect — no env vars.\n\n" +
			"The blob CONTAINS the bearer secret (encoded, not encrypted) — treat it like a\n" +
			"password, send it over a trusted channel, and `dejima token revoke` to kill a leak.\n\n" +
			"--host is the daemon address the teammate will dial (e.g. a tailnet name or LAN\n" +
			"ip:port); the daemon can't know its own external address, so you supply it.",
		RunE: func(cmd *cobra.Command, args []string) error {
			role = strings.TrimSpace(role)
			if role == "" {
				return fmt.Errorf("--role is required (owner, operator, or viewer)")
			}
			host = strings.TrimSpace(host)
			if host == "" {
				// The daemon can't self-detect its reachable address, but when this
				// command runs ON the host we can read the tailnet name/IP directly.
				// Prefer the MagicDNS name (stable across tailnets) over a raw IP.
				if h, _, ok := daemonInviteHost(); ok {
					host = h
					fmt.Fprintf(os.Stderr, "note: --host not given; using this host's tailnet address %s\n", host)
				} else {
					return fmt.Errorf("--host is required (the daemon host:port the teammate will dial, e.g. a MagicDNS name minion.tailXXXX.ts.net:%s or ip:port)", defaultDaemonTCPPort)
				}
			}
			if isRawTailscaleIP(host) {
				fmt.Fprintf(os.Stderr, "warning: --host %s is a raw tailnet IP. If you reach teammates via node-sharing,\n"+
					"         their Tailscale may re-address this node to a different 100.x IP and the invite\n"+
					"         will time out. Prefer this host's MagicDNS name (*.ts.net) — enable MagicDNS if off.\n", hostOnly(host))
			}
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.CreateToken(cmd.Context(), api.CreateTokenRequest{Label: label, Role: role, Owner: owner, Islands: islands})
			if err != nil {
				return err
			}
			blob, err := invite.Encode(invite.Payload{
				Host:    host,
				Token:   resp.Secret,
				Role:    resp.Token.Role,
				Islands: resp.Token.Islands,
				Name:    strings.TrimSpace(name),
				Label:   resp.Token.Label,
			})
			if err != nil {
				// The token was minted but the invite couldn't be built — tell the
				// operator so they can revoke the now-orphaned token.
				return fmt.Errorf("token %s minted but encoding the invite failed: %w (revoke it with `dejima token revoke %s`)", resp.Token.ID, err, resp.Token.ID)
			}
			scope := "all islands"
			if len(resp.Token.Islands) > 0 {
				scope = strings.Join(resp.Token.Islands, ", ")
			}
			fmt.Printf("invite for token %s (role: %s; scope: %s)\n\n", resp.Token.ID, resp.Token.Role, scope)
			fmt.Println("  send this to your teammate (it carries the secret — treat like a password):")
			fmt.Printf("    %s\n\n", blob)
			fmt.Println("  they run:")
			fmt.Println("    dejima join <blob>")
			fmt.Printf("\n  revoke anytime: dejima token revoke %s\n", resp.Token.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&role, "role", "", "owner, operator, or viewer (required)")
	cmd.Flags().StringVar(&host, "host", "", "daemon host:port the teammate dials (default: this host's MagicDNS name / tailnet IP when run on the host)")
	cmd.Flags().StringVar(&owner, "owner", "", "tenant the teammate acts as — they see + create only this tenant's islands (default: the host owner, i.e. full access)")
	cmd.Flags().StringVar(&label, "label", "", "human label for the token (e.g. amanda, phone)")
	cmd.Flags().StringVar(&name, "name", "", "suggested profile name for the teammate (default: host's first label)")
	cmd.Flags().StringArrayVar(&islands, "island", nil, "limit the invite to this island (repeatable); default: all islands")
	return cmd
}

// newJoinCmd is the teammate side: decode an invite blob and persist it as the
// active connection profile (host + token), so subsequent commands Just Work
// with no env vars. Twin of the TUI Team view's paste-to-join.
func newJoinCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "join <invite>",
		Short: "Connect using a team invite blob (saves the host + token as the active profile).",
		Long: "Accept a `dejima token invite` blob: decodes it, saves a connection profile\n" +
			"carrying the daemon host + bearer token, and makes it active. After joining,\n" +
			"`dejima ls`, `dejima status`, etc. connect to that daemon with no env vars.\n\n" +
			"The token is stored in ~/.dejima/client.json (0600). Revoke access from the\n" +
			"issuing side with `dejima token revoke`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := invite.Decode(args[0])
			if err != nil {
				return err
			}
			name, err := clientcfg.SaveInvite(p)
			if err != nil {
				return err
			}
			scope := "all islands"
			if len(p.Islands) > 0 {
				scope = strings.Join(p.Islands, ", ")
			}
			fmt.Printf("joined %s as %s (scope: %s) — saved as profile %q and made active.\n", p.Host, p.Role, scope, name)
			// Preflight the connection so a Tailscale-pinned daemon doesn't leave the
			// teammate to hit an opaque "context deadline exceeded" on the next
			// command. Probe the invite's OWN host directly (tcpReachable) rather than
			// going through resolveHost/Health — the latter can resolve a different
			// target and mask an unreachable tailnet host as "fine". The profile is
			// saved regardless, so a retry after they get on the tailnet Just Works.
			if !tcpReachable(p.Host) {
				if isTailscaleHost(p.Host) {
					printTailscaleJoinHelp(p.Host)
					return nil
				}
				fmt.Printf("note: couldn't reach %s yet — the profile is saved; retry with `dejima ls` or `dejima`.\n", p.Host)
				return nil
			}
			fmt.Println("next: `dejima ls` to see islands, or `dejima` for the TUI.")
			return nil
		},
	}
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
