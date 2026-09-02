package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/clientcfg"
)

// Ephemeral connection overrides set by the root command's `-p/--profile` and
// `--host` flags. They steer THIS process only and are deliberately *never*
// written back to client.json: concurrent TUIs share that file, so persisting a
// per-invocation choice would let one launch stomp another's active profile.
// resolveTarget consults these (via ephemeralTarget) ahead of DEJIMA_HOST and
// the saved profile.
var (
	flagProfile string // -p/--profile NAME — resolved against saved profiles, in-memory
	flagHost    string // --host host:port — a one-off remote target, headless
)

// registerProfileFlags wires the ephemeral launch flags onto the root command.
// They're persistent so they attach to every subcommand (e.g. `dejima -p cloud
// ls`). Resolution happens lazily in ephemeralTarget, not here, so a bad
// profile name surfaces only when a target is actually needed (and `dejima
// profile ls` still works without a valid -p).
func registerProfileFlags(root *cobra.Command) {
	root.PersistentFlags().StringVarP(&flagProfile, "profile", "p", "",
		"connect via a saved profile for this command only (does not change the active profile)")
	root.PersistentFlags().StringVar(&flagHost, "host", "",
		"connect to this daemon (host:port) for this command only; for headless/scripted use")
}

// ephemeralTarget resolves the `-p/--profile` and `--host` launch flags to a
// connection target, reading the saved config but never writing it. ok is false
// when neither flag is set, leaving resolveTarget's normal precedence intact.
//
// --host wins over -p when both are given (a literal address is more explicit
// than a named profile). A -p that names no saved profile is fatal: an explicit
// typo should fail loudly rather than silently fall through to some other
// target.
func ephemeralTarget() (host, label string, ok bool) {
	if h := strings.TrimSpace(flagHost); h != "" {
		return normalizeHost(h), h, true
	}
	name := strings.TrimSpace(flagProfile)
	if name == "" {
		return "", "", false
	}
	cfg, _ := clientcfg.Load()
	h, err := cfg.LookupProfile(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dejima: %v\n", err)
		os.Exit(1)
	}
	return h, name, true
}

// newProfileCmd is the CLI face of the TUI connection switcher: list saved
// profiles, add one, and switch the *persistent* active profile. It reuses
// internal/clientcfg as the single store, so the CLI and TUI never diverge.
// (The ephemeral `-p` flag is the non-persistent sibling of `switch`.)
func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage saved connection profiles (CLI parity with the TUI switcher).",
		Long: "Profiles are saved daemon targets (the local socket, or a remote host:port).\n" +
			"`switch` persists the active profile (shared by every dejima invocation);\n" +
			"the root `-p NAME` flag selects one for a single command without persisting it.",
	}
	cmd.AddCommand(newProfileLsCmd(), newProfileAddCmd(), newProfileSwitchCmd(), newProfileRmCmd())
	return cmd
}

func newProfileLsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List saved profiles, marking the active one.",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := clientcfg.Load()
			if err != nil {
				return err
			}
			// The synthetic "local" target mirrors the TUI list (index 0).
			profiles := append([]clientcfg.Profile{{Name: "local", Host: ""}}, cfg.Profiles...)
			active := cfg.ActiveProfile
			if active == "" {
				active = "local"
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ACTIVE\tNAME\tTARGET")
			for _, p := range profiles {
				marker := " "
				if p.Name == active {
					marker = "*"
				}
				target := p.Host
				if target == "" {
					target = "unix socket"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", marker, p.Name, target)
			}
			return w.Flush()
		},
	}
}

func newProfileAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <host:port>",
		Short: "Save a remote daemon as a profile.",
		Long: "Save a named remote target. A bare host with no :port gets the default\n" +
			"daemon port (7273) added automatically — the same normalization the TUI does.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			host := normalizeHost(args[1])
			if host == "" {
				return fmt.Errorf("host is required (e.g. 100.101.102.103:7273)")
			}
			if err := clientcfg.AddProfile(name, host); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added profile %q → %s\n", name, host)
			return nil
		},
	}
}

func newProfileRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a saved profile (CLI parity with the TUI switcher's [d]).",
		Long: "Remove a saved remote target. If it was the active profile, the active\n" +
			"selection falls back to the local socket. \"local\" can't be removed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if err := clientcfg.RemoveProfile(name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed profile %q\n", name)
			return nil
		},
	}
}

func newProfileSwitchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <name>",
		Short: "Persist the active profile (use \"local\" for the Unix socket).",
		Long: "Persist the active connection profile in client.json. Every subsequent\n" +
			"dejima invocation uses it until switched again. For a one-off target that\n" +
			"does NOT persist (so it can't stomp a shared active profile), use `-p NAME`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if err := clientcfg.SwitchProfile(name); err != nil {
				return err
			}
			if name == "" || name == "local" {
				fmt.Fprintln(cmd.OutOrStdout(), "switched to local (unix socket)")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "switched active profile to %q\n", name)
			}
			return nil
		},
	}
}
