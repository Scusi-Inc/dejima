package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/githubid"
)

// `dejima github ls | default | rm` — managing the identities `connect` creates.
//
// The daemon could ADD GitHub identities and not manage them, and that gap cost
// an operator an hour on an HTTP 401 he could see the cause of and not act on:
//
//	* aoos      <- default, expired token
//	  github    <- brand new, unused
//
// `dejima github connect` without --default creates a SECOND identity, and the
// resolver picks the DEFAULT rather than the newest. Both facts were visible in
// `dejima auth status`, including the `*`. A list you cannot act on is where this
// went wrong — not a missing list.
//
// Related: #18, repointing an ISLAND's GitHub identity, which is the same gap one
// level down.

func newGithubIdentityListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List the daemon's GitHub identities.",
		Long: "Shows every GitHub identity this daemon holds, which one is the DEFAULT\n" +
			"(the one used when nothing names an identity explicitly), and which are\n" +
			"shared with other tenants' islands. Tokens are never shown.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			ids, err := c.ListGitHubIdentities(cmd.Context())
			if err != nil {
				return err
			}
			if len(ids) == 0 {
				fmt.Println("no GitHub identities")
				fmt.Println("  add one with:  dejima github connect --default")
				return nil
			}
			fmt.Printf("%-3s %-20s %-20s %-16s %s\n", "", "NAME", "LOGIN", "HOST", "SHARED")
			for _, m := range ids {
				mark := " "
				if m.Default {
					mark = "*"
				}
				host := m.Host
				if host == "" {
					host = "github.com"
				}
				shared := ""
				if m.Shared {
					shared = "shared"
				}
				fmt.Printf("%-3s %-20s %-20s %-16s %s\n", mark, m.Name, m.Login, host, shared)
			}
			fmt.Println()
			if defaultIdentity(ids) == "" {
				// The state that produced the 401: identities exist and none is
				// default, so every lookup that doesn't name one resolves nothing.
				fmt.Println("⚠  NO DEFAULT IS SET, so anything that doesn't name an identity has none")
				fmt.Println("   to use. Pick one:  dejima github default <name>")
			} else {
				fmt.Println("* is the default — used when nothing names an identity.")
				fmt.Println("Change it with:  dejima github default <name>")
			}
			return nil
		},
	}
}

func newGithubIdentityDefaultCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "default <name>",
		Short: "Choose which GitHub identity is used by default.",
		Long: "Points the default at an identity you already have. The default is what\n" +
			"the daemon resolves when nothing names an identity — island clones, the\n" +
			"release check, `gh` inside an island.\n\n" +
			"This is the command that was missing: `github connect` without --default\n" +
			"adds a second identity and changes nothing about which one is used.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			c, err := client()
			if err != nil {
				return err
			}
			before, _ := c.ListGitHubIdentities(cmd.Context())
			prev := defaultIdentity(before)

			ids, err := c.SetGitHubDefaultIdentity(cmd.Context(), name)
			if err != nil {
				return err
			}
			if prev == name {
				fmt.Printf("%s was already the default\n", name)
				return nil
			}
			if prev == "" {
				fmt.Printf("default GitHub identity set to %s\n", name)
			} else {
				fmt.Printf("default GitHub identity: %s → %s\n", prev, name)
			}
			for _, m := range ids {
				if m.Name == name {
					fmt.Printf("  login %s\n", m.Login)
				}
			}
			// A running daemon resolves per call, but anything holding an already-
			// resolved token keeps it. Say so rather than let a stale 401 read as
			// the change not working — which is the shape that started this.
			fmt.Println()
			fmt.Println("New lookups use it immediately. An island that already has a credential")
			fmt.Println("mounted keeps the old one until it is recreated.")
			return nil
		},
	}
}

func newGithubIdentityRemoveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a GitHub identity.",
		Long: "Deletes a stored GitHub identity. Refuses if islands still reference it,\n" +
			"or if it is the default and removing it would leave none — pass --force to\n" +
			"do it anyway.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			c, err := client()
			if err != nil {
				return err
			}

			// PRE-FLIGHT. The daemon reports which islands were affected AFTER
			// deleting, which is too late to decline. Removing a credential is not
			// reversible from here — the token is gone — so the consequences are
			// worked out first and the operator gets to say no.
			ids, listErr := c.ListGitHubIdentities(cmd.Context())
			islands, islErr := c.ListIslands(cmd.Context())
			blockers := removalBlockers(name, ids, islands, listErr != nil || islErr != nil)

			if len(blockers) > 0 && !force {
				fmt.Fprintf(os.Stderr, "refusing to remove %q:\n", name)
				for _, b := range blockers {
					fmt.Fprintf(os.Stderr, "  - %s\n", b)
				}
				fmt.Fprintln(os.Stderr)
				return fmt.Errorf("re-run with --force to remove it anyway")
			}
			if len(blockers) > 0 {
				fmt.Printf("removing %q anyway (--force):\n", name)
				for _, b := range blockers {
					fmt.Printf("  - %s\n", b)
				}
				fmt.Println()
			}

			affected, err := c.DeleteGitHubIdentity(cmd.Context(), name)
			if err != nil {
				return err
			}
			fmt.Printf("removed GitHub identity %s\n", name)
			// The daemon's list is authoritative and may differ from the pre-flight
			// (another client could have changed things in between), so it is
			// reported rather than assumed to match.
			if len(affected) > 0 {
				fmt.Printf("  still referenced by: %s\n", strings.Join(affected, ", "))
				fmt.Println("  those islands lose this auth on their next recreate")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove even if islands use it or it is the default")
	return cmd
}

// removalBlockers lists the reasons not to remove an identity, in the words the
// operator will read. Split out from the command so it can be tested: the guard
// is the whole value of `rm`, and a guard nothing exercises is decoration.
//
// unknown is true when the daemon did not answer one of the lookups. That is
// reported as a blocker rather than skipped, because "I couldn't check" must not
// read as "nothing to worry about" — a pre-flight that silently didn't run looks
// exactly like one that found nothing.
func removalBlockers(name string, ids []githubid.Meta, islands []api.IslandInfo, unknown bool) []string {
	var blockers []string
	var users []string
	for _, i := range islands {
		if i.GitHubIdentity == name {
			users = append(users, i.Name)
		}
	}
	if len(users) > 0 {
		blockers = append(blockers,
			fmt.Sprintf("%d island(s) still use it: %s — they lose this auth at their next recreate",
				len(users), strings.Join(users, ", ")))
	}
	if defaultIdentity(ids) == name && len(ids) > 1 {
		blockers = append(blockers,
			"it is the DEFAULT — removing it leaves no default, and anything that doesn't "+
				"name an identity will resolve none (set another first: dejima github default <name>)")
	}
	if unknown {
		blockers = append(blockers,
			"couldn't check what depends on this identity (the daemon didn't answer), so the "+
				"consequences are unknown rather than none")
	}
	return blockers
}

// defaultIdentity returns the name of the default identity, or "" if none is.
func defaultIdentity(ids []githubid.Meta) string {
	for _, m := range ids {
		if m.Default {
			return m.Name
		}
	}
	return ""
}
