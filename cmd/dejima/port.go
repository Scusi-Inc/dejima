package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// --- port -----------------------------------------------------------------

func newPortCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port",
		Short: "Grant an island brokered access to host files (Port scopes).",
		Long: "The Port is the host-side broker that mediates host-file access for an island. " +
			"Access is deny-all by default; `port grant` opens a scoped, audited window onto a " +
			"host directory. Every crossing is recorded in the append-only Ledger. V1 is " +
			"read-only and copy-based (see docs/port-island-spec.md).",
	}
	cmd.AddCommand(newPortGrantCmd(), newPortListCmd(), newPortRevokeCmd(),
		newPortIntakeCmd(), newPortExportCmd())
	return cmd
}

func newPortGrantCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "grant <island> <host-path>[:ro]",
		Short: "Grant an island brokered read-only access to a host directory.",
		Long: "Grants <island> brokered access to an absolute host directory. The path is " +
			"resolved on the daemon host. Append :ro to be explicit (read-only is the only " +
			"mode in V1).\n\n  dejima port grant myrepo ~/Obsidian/Vault:ro",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			hostPath, mode := splitScopeMode(args[1])
			if !filepath.IsAbs(hostPath) {
				return fmt.Errorf("host path must be absolute: %q", hostPath)
			}
			c, err := client()
			if err != nil {
				return err
			}
			scope, err := c.GrantPortScope(cmd.Context(), island, hostPath, mode)
			if err != nil {
				return err
			}
			fmt.Printf("granted %q (%s) on %s → scope %q\n", scope.HostPath, scope.Mode, island, scope.Name)
			return nil
		},
	}
}

func newPortListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <island>",
		Short: "List an island's Port scopes.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			resp, err := c.ListPortScopes(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if len(resp.Scopes) == 0 {
				fmt.Printf("%s has no Port scopes (deny-all: no host files reachable)\n", args[0])
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SCOPE\tMODE\tHOST PATH\tGRANTED")
			for _, sc := range resp.Scopes {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					sc.Name, sc.Mode, sc.HostPath, sc.GrantedAt.Local().Format("2006-01-02 15:04"))
			}
			return tw.Flush()
		},
	}
}

func newPortRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <island> <scope-or-host-path>",
		Short: "Revoke a Port scope.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, key := args[0], args[1]
			c, err := client()
			if err != nil {
				return err
			}
			// Resolve a host-path argument to its scope name (the delete route
			// addresses scopes by name, which carry no slashes).
			resp, err := c.ListPortScopes(cmd.Context(), island)
			if err != nil {
				return err
			}
			name := ""
			cleaned := filepath.Clean(key)
			for _, sc := range resp.Scopes {
				if sc.Name == key || sc.HostPath == cleaned {
					name = sc.Name
					break
				}
			}
			if name == "" {
				return fmt.Errorf("no Port scope matching %q on %s", key, island)
			}
			if err := c.RevokePortScope(cmd.Context(), island, name); err != nil {
				return err
			}
			fmt.Printf("revoked scope %q on %s\n", name, island)
			return nil
		},
	}
}

func newPortIntakeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "intake <island> <scope>:<path> [container-dest]",
		Short: "Broker a host file (within a scope) into an island, read-only.",
		Long: "Copies a file from a granted scope into the island. <path> is relative to " +
			"the scope's host root. The crossing is recorded in the Ledger; if it can't be " +
			"logged, the file does not cross.\n\n  dejima port intake myrepo vault:daily/2026-06-11.md",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			scope, rel, ok := strings.Cut(args[1], ":")
			if !ok || scope == "" || rel == "" {
				return fmt.Errorf("expected <scope>:<path>, got %q", args[1])
			}
			dest := ""
			if len(args) == 3 {
				dest = args[2]
			}
			c, err := client()
			if err != nil {
				return err
			}
			res, err := c.PortIntake(cmd.Context(), island, scope, rel, dest)
			if err != nil {
				return err
			}
			fmt.Printf("intake %s → %s (%d bytes, sha256:%s)\n", res.Src, res.Dest, res.Bytes, res.SHA256[:12])
			return nil
		},
	}
}

func newPortExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "export <island> <container-path>",
		Short: "Broker a file out of an island into host export staging.",
		Long: "Copies a file out of the island into ~/.dejima/projects/<island>/exports/. " +
			"It never writes into a user scope (that is the read-write milestone), so it is " +
			"safe under read-only V1. The crossing is recorded in the Ledger.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			res, err := c.PortExport(cmd.Context(), args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Printf("export %s → %s (%d bytes, sha256:%s)\n", res.Src, res.Dest, res.Bytes, res.SHA256[:12])
			return nil
		},
	}
}

// splitScopeMode parses a "<path>[:ro|:rw]" argument. A missing or unrecognized
// suffix defaults to read-only.
func splitScopeMode(arg string) (path, mode string) {
	if i := strings.LastIndex(arg, ":"); i >= 0 {
		switch arg[i+1:] {
		case "ro":
			return arg[:i], "ro"
		case "rw":
			return arg[:i], "rw"
		}
	}
	return arg, "ro"
}
