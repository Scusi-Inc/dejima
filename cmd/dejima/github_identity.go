package main

import (
	"fmt"
	"os"
	"strings"
	"time"

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
			resp, err := c.ListGitHubIdentitiesFull(cmd.Context())
			if err != nil {
				return err
			}
			ids := resp.Identities
			if len(ids) == 0 {
				fmt.Println("no GitHub identities")
				fmt.Println("  add one with:  dejima github connect --default")
				return nil
			}
			fmt.Printf("%-3s %-20s %-20s %-16s %-10s %s\n", "", "NAME", "LOGIN", "HOST", "REFRESHED", "ISLANDS")
			for _, m := range ids {
				mark := " "
				if m.Default {
					mark = "*"
				}
				host := m.Host
				if host == "" {
					host = "github.com"
				}
				fmt.Printf("%-3s %-20s %-20s %-16s %-10s %s\n",
					mark, m.Name, m.Login, host, refreshedAge(m.UpdatedAt), islandsCell(m.Islands))
				// What the token may DO, under the row it belongs to. "It
				// authenticates" was the strongest thing this listing could say,
				// so a token that could clone and push but not open a pull request
				// was indistinguishable from a working one until an agent hit
				// "Resource not accessible by personal access token".
				if note, canWrite := githubid.ScopeNote(m.Scopes); !canWrite {
					fmt.Printf("        scopes: %s — cannot push or open PRs\n", note)
				} else if m.Scopes != "" {
					fmt.Printf("        scopes: %s\n", note)
				}
			}
			fmt.Println()
			for _, d := range resp.Dangling {
				fmt.Printf("⚠  island %q names identity %q, which this daemon does not have.\n", d.Island, d.Identity)
				fmt.Println("   It materializes NO credential, so git in it fails the same way an")
				fmt.Println("   expired token does. Repoint it:  dejima github repoint " + d.Island + " <name>")
			}
			if len(resp.Dangling) > 0 {
				fmt.Println()
			}
			def := defaultIdentity(metasOfViews(ids))
			if def == "" {
				// The state that produced the 401: identities exist and none is
				// default, so every lookup that doesn't name one resolves nothing.
				fmt.Println("⚠  NO DEFAULT IS SET, so anything that doesn't name an identity has none")
				fmt.Println("   to use. Pick one:  dejima github default <name>")
				return nil
			}
			// The line this listing existed without, and the reason an operator
			// refreshed the right-looking identity and fixed nothing. `*` marks
			// the default; it does NOT mean the default is what anything uses.
			if unused, used := splitByUsage(ids, def); unused != "" {
				fmt.Printf("⚠  THE DEFAULT (%s) IS USED BY NO ISLAND — refreshing it changes nothing.\n", unused)
				if used != "" {
					fmt.Printf("   Islands resolve %q instead. To refresh the one they actually use:\n", used)
					fmt.Printf("     dejima github connect %s --default\n", used)
				}
				fmt.Println()
			}
			fmt.Println("* is the default — used only by islands that name no identity of their own.")
			fmt.Println("Change it with:      dejima github default <name>")
			fmt.Println("Repoint an island:   dejima github repoint <island> <identity>")
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

// refreshedAge renders when an identity's token was last written. A ZERO time
// means the identity predates the field, and it renders as "unknown" — not as
// "just now" and not as 1970. An operator reading this column is deciding
// whether a credential is plausibly dead, and a confident wrong answer there is
// worse than admitting the daemon does not know.
func refreshedAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return timeAgo(t) + " ago"
}

// islandsCell summarises which islands resolve to an identity, capped so a host
// with thirty islands still prints a table. The COUNT is always exact and comes
// first — a truncated list must never be readable as the whole list.
func islandsCell(islands []string) string {
	switch n := len(islands); {
	case n == 0:
		return "—  (none)"
	case n <= 3:
		return fmt.Sprintf("%d  (%s)", n, strings.Join(islands, ", "))
	default:
		return fmt.Sprintf("%d  (%s, +%d more)", n, strings.Join(islands[:3], ", "), n-3)
	}
}

// splitByUsage reports the exact trap this listing was blind to: the default
// identity is used by NO island while some other identity is used by several.
// Refreshing the default then looks completely correct and reaches nothing.
//
// It returns ("", "") unless that specific shape holds. A default with no
// islands on a host with no islands at all is not this bug, and neither is a
// default that everything uses. Only the divergence is worth a warning — a
// caution that fires on healthy states is one people learn to skip.
func splitByUsage(ids []api.GitHubIdentityView, def string) (unusedDefault, mostUsed string) {
	best := 0
	for _, m := range ids {
		if m.Name == def {
			if len(m.Islands) > 0 {
				return "", "" // the default is in use; nothing to warn about
			}
			continue
		}
		if len(m.Islands) > best {
			best, mostUsed = len(m.Islands), m.Name
		}
	}
	if best == 0 {
		return "", "" // nothing uses anything — a fresh daemon, not a misdirection
	}
	return def, mostUsed
}

// metasOfViews drops the island decoration for the helpers that predate it.
func metasOfViews(views []api.GitHubIdentityView) []githubid.Meta {
	out := make([]githubid.Meta, 0, len(views))
	for _, v := range views {
		out = append(out, v.Meta)
	}
	return out
}

// newGithubRepointCmd changes WHICH stored identity an island uses.
//
// The gap this closes, stated plainly: an island's GitHub identity was chosen at
// create time and after that the only way to change it was to edit
// ~/.dejima/projects/<name>/config.toml by hand on the host. So when an
// identity's token expired, "point these islands at the working credential" was
// not an operation that existed — the only supported move was to refresh that
// exact identity, and nothing on any surface said which islands cared.
func newGithubRepointCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repoint <island> <identity>",
		Short: "Point an island at a different GitHub identity.",
		Long: "Changes which of the daemon's GitHub identities an island clones and pushes\n" +
			"as, and re-materializes the island's credential immediately — no recreate,\n" +
			"and no restart of whatever the agents are doing.\n\n" +
			"Pass \"\" as the identity to follow the daemon default instead of naming one.\n\n" +
			"See who uses what first:  dejima github ls",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := client()
			if err != nil {
				return err
			}
			island, identity := args[0], strings.TrimSpace(args[1])
			out, err := c.SetIslandGitHubIdentity(cmd.Context(), island, identity)
			if err != nil {
				return err
			}
			if out.Identity == "" {
				fmt.Printf("%s now follows the default GitHub identity → %s (%s)\n",
					out.Island, out.Resolved, out.Login)
			} else {
				fmt.Printf("%s now uses GitHub identity %s (%s)\n", out.Island, out.Resolved, out.Login)
			}
			// The credential is live in the container already; say so, because the
			// reflex after any credential change here is to run `dejima upgrade`,
			// and that recreates the container and kills agent sessions.
			fmt.Println("The island's gh credential was refreshed in place — no upgrade needed.")
			return nil
		},
	}
}
