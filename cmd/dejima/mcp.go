package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
)

// --- mcp (MCP broker) ------------------------------------------------------
//
// Deny-by-default grants of named, host-curated MCP (Model Context Protocol)
// servers into an island, plus a brokered, ledgered call path. Mirrors
// `dejima cap`: the host curates the servers (~/.dejima/mcp/servers.toml), the
// operator grants an island a named server, and every grant/call is recorded in
// the append-only Ledger (`mcp.*`). See docs/mcp-broker-spec.md.

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mcp",
		Aliases: []string{"mcpbroker"},
		Short:   "Broker audited MCP-server access into an island (deny-all by default).",
		Long: "The MCP broker lets a contained agent reach a curated MCP server — authored " +
			"host-side in ~/.dejima/mcp/servers.toml — by name, with every call mediated and " +
			"recorded in the append-only Ledger. Access is deny-all by default; `mcp grant` " +
			"opens a single named server to one island. See docs/mcp-broker-spec.md.",
	}
	cmd.AddCommand(newMCPGrantCmd(), newMCPListCmd(), newMCPRevokeCmd(), newMCPCallCmd())
	return cmd
}

func newMCPGrantCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "grant <island> <server>",
		Short: "Grant an island permission to invoke a named host MCP server.",
		Long: "Grants <island> permission to invoke <server> — a server named in " +
			"~/.dejima/mcp/servers.toml. The server need not be registered yet; a call " +
			"fails closed until it is.\n\n  dejima mcp grant oc-home files",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, server := args[0], args[1]
			c, err := client()
			if err != nil {
				return err
			}
			grant, err := c.GrantMCP(cmd.Context(), island, server)
			if err != nil {
				return err
			}
			fmt.Printf("granted mcp server %q on %s\n", grant.Server, island)
			return nil
		},
	}
}

func newMCPListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <island>",
		Aliases: []string{"ls"},
		Short:   "List an island's granted MCP servers.",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.ListMCPGrants(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(resp.Grants) == 0 {
				fmt.Printf("%s has no MCP grants (deny-all: no MCP servers reachable)\n", args[0])
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SERVER\tGRANTED")
			for _, g := range resp.Grants {
				fmt.Fprintf(tw, "%s\t%s\n", g.Server, g.GrantedAt.Local().Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}
}

func newMCPRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <island> <server>",
		Short: "Revoke an MCP-server grant.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, server := args[0], args[1]
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.RevokeMCP(cmd.Context(), island, server); err != nil {
				return err
			}
			fmt.Printf("revoked mcp server %q on %s\n", server, island)
			return nil
		},
	}
}

func newMCPCallCmd() *cobra.Command {
	var method, params string
	cmd := &cobra.Command{
		Use:   "call <island> <server>",
		Short: "Make a brokered, ledgered MCP call to a granted server.",
		Long: "Brokers one JSON-RPC call to <server> on behalf of <island>. The server must " +
			"be granted (`mcp grant`) and registered host-side; the method must be in the " +
			"brokered surface (tools/list, tools/call, resources/list, resources/read, " +
			"prompts/list, prompts/get). Every call is recorded in the Ledger.\n\n" +
			"  dejima mcp call oc-home files --method tools/list\n" +
			`  dejima mcp call oc-home files --method tools/call --params '{"name":"fetch","arguments":{"url":"…"}}'`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, server := args[0], args[1]
			req := api.MCPCallRequest{Island: island, Server: server, Method: method}
			if params != "" {
				if !json.Valid([]byte(params)) {
					return fmt.Errorf("--params is not valid JSON")
				}
				req.Params = json.RawMessage(params)
			}
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.CallMCP(cmd.Context(), req)
			if err != nil {
				return err
			}
			if len(resp.Result) > 0 {
				if b, err := json.MarshalIndent(resp.Result, "", "  "); err == nil {
					fmt.Println(string(b))
				} else {
					fmt.Println(string(resp.Result))
				}
			}
			if resp.IsError {
				fmt.Fprintln(os.Stderr, "note: the server reported an application error (isError)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&method, "method", "tools/list", "JSON-RPC method (brokered surface only)")
	cmd.Flags().StringVar(&params, "params", "", "JSON-RPC params object (JSON)")
	return cmd
}
