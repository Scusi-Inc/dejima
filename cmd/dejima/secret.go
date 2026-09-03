package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"golang.org/x/term"
)

// `dejima secret` — per-island storage for the access tokens an agent's tools
// need, so they stop living in the repo, a shell profile, or a chat message.
//
// The copy here deliberately states that agents in the island can read these.
// Overclaiming would be worse than shipping nothing: an operator who believed
// values were hidden would store things that don't belong in an agent's
// container. See docs/secrets-manager-spec.md.

func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage an island's secrets (tokens its tools need).",
		Long: "Per-island storage for the access tokens an agent's tools read from the\n" +
			"environment — EXPO_TOKEN, NPM_TOKEN, API keys. Values are held by the daemon\n" +
			"(OS keychain where available) and injected into the island; they are never\n" +
			"shown again and never returned over the API.\n\n" +
			"Note: this is not a boundary against agents. Everything in an island runs as\n" +
			"one user, so any agent there can read these values from its own environment.\n" +
			"What you get is that they're out of your repo and your chat history, in one\n" +
			"place to rotate and revoke, scoped to a single island, and deleted with it.\n" +
			"Prefer narrowly-scoped, rotatable tokens.",
	}
	cmd.AddCommand(newSecretSetCmd(), newSecretListCmd(), newSecretRemoveCmd())
	return cmd
}

func newSecretSetCmd() *cobra.Command {
	var fromStdin bool
	cmd := &cobra.Command{
		Use:   "set <island> <NAME>",
		Short: "Set or rotate a secret (prompts for the value).",
		Long: "Stores a value under NAME for <island>. With no --stdin the value is read\n" +
			"from the terminal with echo off, so it stays out of your shell history and\n" +
			"the process list.\n\n" +
			"Setting an existing NAME rotates it: the value and fingerprint change, the\n" +
			"original creation date is kept.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, key := args[0], args[1]
			c, err := client()
			if err != nil {
				return err
			}

			var value string
			switch {
			case fromStdin:
				b, rerr := io.ReadAll(os.Stdin)
				if rerr != nil {
					return fmt.Errorf("read value from stdin: %w", rerr)
				}
				// Trim only the trailing newline a pipe or heredoc adds; interior
				// whitespace can be part of the value.
				value = strings.TrimRight(string(b), "\r\n")
			default:
				if !term.IsTerminal(int(os.Stdin.Fd())) {
					return fmt.Errorf("no terminal to prompt on — pipe the value instead:\n" +
						"  echo <value> | dejima secret set <island> <NAME> --stdin")
				}
				fmt.Printf("Value for %s (input hidden): ", key)
				b, rerr := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Println()
				if rerr != nil {
					return fmt.Errorf("read value: %w", rerr)
				}
				value = strings.TrimSpace(string(b))
			}
			if value == "" {
				return fmt.Errorf("no value entered")
			}

			meta, err := c.PutSecret(cmd.Context(), island, key, value)
			if err != nil {
				return err
			}
			fmt.Printf("set %s on %s (fingerprint %s)\n", meta.Name, island, meta.Fingerprint)
			fmt.Println()
			// The restart caveat is not a footnote: a running process cannot have
			// its environment changed, so without this the operator watches their
			// agent keep failing with the old value and concludes it didn't work.
			fmt.Println("⚠  RESTART TERMINALS TO APPLY")
			fmt.Printf("   It's live in NEW shells in %s; anything already running still has the\n", island)
			fmt.Println("   old environment. Restart the agent to pick it up.")
			warnGitHubTokenPrecedence(cmd.Context(), c, island, meta.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the value from stdin (keeps it out of shell history and the process list)")
	return cmd
}

func newSecretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "ls <island>",
		Aliases: []string{"list"},
		Short:   "List an island's secrets (names only — values are never shown).",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			island := args[0]
			c, err := client()
			if err != nil {
				return err
			}
			metas, err := c.ListSecrets(cmd.Context(), island)
			if err != nil {
				return err
			}
			if len(metas) == 0 {
				fmt.Printf("no secrets on %s\n", island)
				fmt.Printf("  add one with:  dejima secret set %s <NAME>\n", island)
				return nil
			}
			fmt.Printf("%-24s %-12s %-12s %s\n", "NAME", "FINGERPRINT", "ADDED", "ROTATED")
			for _, m := range metas {
				rotated := "—"
				if m.UpdatedAt.After(m.CreatedAt) {
					rotated = m.UpdatedAt.Local().Format("2006-01-02")
				}
				fmt.Printf("%-24s %-12s %-12s %s\n",
					m.Name, m.Fingerprint, m.CreatedAt.Local().Format("2006-01-02"), rotated)
			}
			fmt.Println()
			fmt.Println("Values are never displayed. The fingerprint is sha256(value)[:8] — hash your")
			fmt.Println("own copy to confirm it matches.")
			return nil
		},
	}
}

func newSecretRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <island> <NAME>",
		Aliases: []string{"remove"},
		Short:   "Remove a secret from an island.",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			island, key := args[0], args[1]
			c, err := client()
			if err != nil {
				return err
			}
			if err := c.DeleteSecret(cmd.Context(), island, key); err != nil {
				return err
			}
			fmt.Printf("removed %s from %s\n", key, island)
			fmt.Println("Already-running processes keep the old value until restarted.")
			return nil
		},
	}
}

// warnGitHubTokenPrecedence flags the one secret name that can silently change
// how an island authenticates to GitHub.
//
// gh prefers GH_TOKEN/GITHUB_TOKEN from the environment over the credential in
// its config dir. So setting one of those names on an island that already has a
// mounted GitHub credential swaps which identity — and which PERMISSIONS — every
// clone, push and `gh` call uses, with no error and no visible change. That is
// worth one line at the moment it becomes true, especially while operators are
// narrowing PAT scopes: the two tokens can differ in what they can reach.
//
// Best-effort by design, but never SILENTLY skipped: if the island's credential
// state can't be determined we still say the conditional form, because "I
// couldn't check" must not render as "there's nothing to tell you".
func warnGitHubTokenPrecedence(ctx context.Context, c *api.Client, island, name string) {
	switch strings.ToUpper(name) {
	case "GH_TOKEN", "GITHUB_TOKEN":
	default:
		return
	}
	has, known := islandHasGitHubCredential(ctx, c, island)
	if known && !has {
		return // no credential to conflict with — nothing to warn about
	}
	fmt.Println()
	if known {
		fmt.Printf("⚠  %s OVERRIDES THIS ISLAND'S GITHUB CREDENTIAL\n", name)
	} else {
		fmt.Printf("⚠  IF THIS ISLAND HAS A GITHUB CREDENTIAL, %s OVERRIDES IT\n", name)
		fmt.Println("   (couldn't check this island's credential state, so this may not apply)")
	}
	fmt.Println("   gh reads this variable in preference to its config, so clone/push and every")
	fmt.Println("   `gh` call will now authenticate as this token instead — with whatever")
	fmt.Println("   permissions it has, which may be more or fewer than before. No error is")
	fmt.Printf("   raised either way. Check with: dejima github host-credential status %s\n", island)
}

// islandHasGitHubCredential reports whether the island has a GitHub credential
// that GH_TOKEN would override, and whether that could be determined at all.
//
// Configured OR mounted, deliberately: a credential granted but pending a
// container recreate is one the operator has decided to have, and staying quiet
// now would mean the override lands silently at the next restart — the warning
// would be exactly one recreate too late.
// The mount is identified by api.GitHubCredentialMountPath, not by a copy of
// the string. A local copy silently stops matching if the daemon's path moves,
// and "no entry matched" is indistinguishable from "this island has no GitHub
// credential" — so the warning would go quiet rather than go wrong.
//
// And no entry matching is UNKNOWN, not NO. Returning known=true there would
// assert something this function did not find out, and the caller's early
// return for known-and-absent prints nothing at all — not even the conditional
// form. That is the one path in here that fails toward silence, which the doc
// above explicitly promises it won't.
func islandHasGitHubCredential(ctx context.Context, c *api.Client, island string) (has, known bool) {
	g, err := c.ListGrants(ctx, island)
	if err != nil {
		return false, false
	}
	return gitHubCredentialFrom(g.Credentials)
}

// gitHubCredentialFrom is the decision itself, split out from the fetch so the
// not-found case is reachable in a test. It cannot be reached through a real
// daemon — credentialMounts() always enumerates the gh entry, so a live report
// carries it with Configured/Mounted false — which is exactly why the branch
// needs a test: it only ever runs when the two sides have drifted apart, i.e.
// when nobody is watching.
func gitHubCredentialFrom(rep api.CredentialMountReport) (has, known bool) {
	if !rep.Known {
		return false, false
	}
	for _, s := range rep.States {
		if s.Path == api.GitHubCredentialMountPath {
			return s.Configured || s.Mounted, true
		}
	}
	return false, false
}
