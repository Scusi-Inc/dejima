package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/aoos/dejima/internal/api"

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
		newPortIntakeCmd(), newPortExportCmd(), newPortWriteCmd())
	return cmd
}

func newPortGrantCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "grant <island> <host-path>[:ro|:rw]",
		Short: "Grant an island brokered access to a host directory (ro default, rw opt-in).",
		Long: "Grants <island> brokered access to an absolute host directory. The path is " +
			"resolved on the daemon host. Default is read-only; append :rw to also allow " +
			"`dejima port write` back into the scope.\n\n  dejima port grant myrepo ~/Obsidian/Vault:ro\n" +
			"  dejima port grant myrepo ~/Obsidian/Vault:rw",
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
	var recursive bool
	var maxFiles int
	var maxBytesStr string
	cmd := &cobra.Command{
		Use:   "intake <island> <scope>:<path> [container-dest]",
		Short: "Broker a host file or folder (within a scope) into an island, read-only.",
		Long: "Copies a file — or with -r a whole directory — from a granted scope into the " +
			"island. <path> is relative to the scope's host root. Every crossing is recorded " +
			"in the Ledger, one entry per file; if it can't be logged, the file does not " +
			"cross.\n\n  dejima port intake myrepo vault:daily/2026-06-11.md\n" +
			"  dejima port intake myrepo vault:daily -r\n\n" +
			"Symlinks are never followed, and any skipped entries are reported.\n\n" +
			"A recursive import is capped (the daemon's defaults are 2000 files / 512 MiB) so " +
			"that pointing it at a home directory fails in a second instead of copying for ten " +
			"minutes. Raise the caps for one import with --max-files / --max-bytes; the refusal " +
			"tells you the tree's actual size on both axes, so one attempt is enough to decide.",
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
			caps := api.PortIntakeCaps{MaxFiles: maxFiles}
			if maxBytesStr != "" {
				n, err := parseSize(maxBytesStr)
				if err != nil {
					return fmt.Errorf("--max-bytes: %w", err)
				}
				caps.MaxBytes = n
			}
			// The caps only exist on the recursive path, so accepting them
			// silently for a single file would be a flag that reports nothing and
			// does nothing — the shape this whole change is removing.
			if !recursive && (caps.MaxFiles > 0 || caps.MaxBytes > 0) {
				return fmt.Errorf("--max-files/--max-bytes apply to a recursive import; add -r")
			}
			c, err := client()
			if err != nil {
				return err
			}
			res, err := c.PortIntakeRecursive(cmd.Context(), island, scope, rel, dest, recursive, caps)
			if err != nil {
				return err
			}
			if !res.Recursive {
				fmt.Printf("intake %s → %s (%d bytes, sha256:%s)\n", res.Src, res.Dest, res.Bytes, res.SHA256[:12])
				return nil
			}
			fmt.Printf("intake %s → %s\n", res.Src, res.Dest)
			fmt.Printf("  %s crossed (%s), ledger batch %s\n",
				countNoun(len(res.Files), "file"), humanBytes(uint64(res.Bytes)), res.BatchID)
			for _, sk := range res.Skipped {
				fmt.Printf("  skipped %s — %s\n", sk.Rel, sk.Reason)
			}
			// A partial import must not exit 0. The files that crossed are real and
			// ledgered and are NOT rolled back — but reporting success over a result
			// that is missing files is the failure this whole surface exists to avoid.
			if len(res.Failed) > 0 {
				for _, f := range res.Failed {
					fmt.Fprintf(os.Stderr, "  FAILED %s — %s\n", f.Rel, f.Error)
				}
				return fmt.Errorf("%s did not cross (%s did); nothing was rolled back",
					countNoun(len(res.Failed), "file"), countNoun(len(res.Files), "file"))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&maxFiles, "max-files", 0,
		"raise the file-count cap for this import (0 = the daemon's default)")
	cmd.Flags().StringVar(&maxBytesStr, "max-bytes", "",
		"raise the total-size cap for this import, e.g. 2G (empty = the daemon's default)")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false,
		"import a directory: one brokered, ledgered crossing per file (symlinks skipped)")
	return cmd
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

func newPortWriteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "write <island> <container-path> <scope>:<path>",
		Short: "Write a file from an island OUT into a read-write host scope.",
		Long: "Copies a file out of the island into a `:rw`-granted host scope. <path> is " +
			"relative to the scope's host root. Symlink/`..` escapes are refused; the crossing " +
			"is recorded in the Ledger (fail-closed).\n\n  dejima port write myrepo /workspace/out.md vault:notes/out.md",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, src := args[0], args[1]
			scope, rel, ok := strings.Cut(args[2], ":")
			if !ok || scope == "" || rel == "" {
				return fmt.Errorf("expected <scope>:<path>, got %q", args[2])
			}
			c, err := client()
			if err != nil {
				return err
			}
			res, err := c.PortWrite(cmd.Context(), island, scope, src, rel)
			if err != nil {
				return err
			}
			fmt.Printf("write %s → %s (%d bytes, sha256:%s)\n", res.Src, res.Dest, res.Bytes, res.SHA256[:12])
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
