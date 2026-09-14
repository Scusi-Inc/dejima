package main

import (
	"context"
	"fmt"
	"strings"

	"errors"
	"os"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/reposrc"
	"golang.org/x/term"
)

// deviceFlowUnconfigured reports whether err is the daemon refusing guided
// sign-in because it has no OAuth app (DEJIMAD_GITHUB_CLIENT_ID unset).
//
// Matched on the message rather than a status code because the client surfaces
// server errors as plain text; this mirrors isGitHubIdentityGateError. The
// daemon's wording is the contract — keep the two in step.
func deviceFlowUnconfigured(err error) bool {
	return err != nil && strings.Contains(err.Error(), "guided GitHub sign-in isn't configured")
}

// connectGitHubViaToken is the fallback when guided sign-in is unavailable,
// which is the norm for a self-hosted daemon: the device flow needs a
// registered OAuth app, so on a stock install it is always dark.
//
// Before, `dejima github connect` simply returned the daemon's 501 and exited
// 1 — the first thing a new operator hit, with no way forward from the command
// they were told to run. The token path was always there; nothing did it for
// them. So do it: if gh is logged in locally, push that identity. If it isn't,
// print the two steps rather than a paragraph.
func connectGitHubViaToken(ctx context.Context, c *api.Client, name string, token string, makeDefault, shared bool) error {
	var login string
	var err error

	switch {
	case strings.TrimSpace(token) != "":
		token = strings.TrimSpace(token) // supplied via --token / --token-stdin

	case reposrc.GitHubAvailable() == nil:
		// gh is here and signed in — read the identity straight off it.
		login, token, err = reposrc.LocalGitHubLogin("")
		if err != nil {
			return err
		}

	default:
		// No device flow, no gh. A self-hosted daemon must still be connectable,
		// so ask for a token directly rather than making the gh CLI a hard
		// dependency of using Dejima at all — which is what it had become.
		token, err = promptForToken()
		if err != nil {
			return err
		}
	}
	// Verify before storing, so a stale or over-narrow token fails here rather
	// than inside an island days later.
	verified, ghUserID, scopes, err := githubid.VerifyToken(ctx, "", token)
	if err != nil {
		return fmt.Errorf("token verification failed (nothing stored): %w", err)
	}
	if verified != "" {
		login = verified
	}
	id := strings.TrimSpace(name)
	if id == "" {
		id = login
	}
	if _, err := c.PutGitHubIdentity(ctx, id, api.PutGitHubIdentityRequest{
		Login: login, ID: ghUserID, Token: token, Default: makeDefault, Shared: shared, Scopes: scopes,
	}); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("connected GitHub identity %q (login %s)\n", id, login)
	// Say what the token CAN DO, here, at the moment it is stored. The comment
	// above claims this call catches an "over-narrow" token; until now it could
	// only catch an INVALID one. A token that authenticates and cannot open a
	// pull request looked identical to a working one, and the difference surfaced
	// hours later inside an island as "Resource not accessible by personal access
	// token" — a message naming nothing anyone could act on.
	if note, canWrite := githubid.ScopeNote(scopes); !canWrite {
		fmt.Println()
		fmt.Printf("⚠ this token's scopes are: %s\n", note)
		fmt.Println("  It can authenticate, but it CANNOT push or open pull requests, so an")
		fmt.Println("  agent will fail with \"Resource not accessible by personal access token\".")
		fmt.Println("  Re-issue it with the `repo` scope (classic), or grant Contents +")
		fmt.Println("  Pull requests write + Workflows (fine-grained), then re-run this command.")
	} else if strings.Contains(note, "workflow") && strings.Contains(note, "⚠") {
		// A working token with one hole in it. Said HERE rather than only in
		// `github ls`, because this is the moment the operator is on GitHub's
		// token page and adding the permission is one checkbox — an hour later
		// it is a re-issue, and at push time it is a failed run with the work
		// already done.
		fmt.Println()
		fmt.Printf("ℹ this token's scopes are: %s\n", note)
		fmt.Println("  Everything else works. Only commits that add or edit files under")
		fmt.Println("  .github/workflows/ are refused — a CI rename or a version bump will")
		fmt.Println("  fail at push. Add the `workflow` scope now if agents here touch CI.")
	}
	fmt.Println("islands can now clone and push as this identity.")
	fmt.Println()
	fmt.Println("check it with: dejima auth status")
	return nil
}

// promptForToken reads a personal access token from the terminal with echo off.
//
// This is the path that makes a self-hosted daemon usable with no OAuth app and
// no gh CLI: guided sign-in needs the former, and every other route needed the
// latter, so a host without gh had no way to connect GitHub at all.
func promptForToken() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("no GitHub identity available and no terminal to ask on.\n\n" +
			"  Provide a token directly:\n" +
			"    dejima github connect --token <token>\n" +
			"    (or pipe it:  echo <token> | dejima github connect --token-stdin)\n\n" +
			"  Create one at https://github.com/settings/tokens — a fine-grained token\n" +
			"  with Contents: Read and Write on the repos the islands should reach,\n" +
			"  plus Workflows: Read and Write if agents there edit .github/workflows/")
	}
	fmt.Println()
	fmt.Println("Connect GitHub with a personal access token")
	fmt.Println()
	fmt.Println("  Guided sign-in isn't configured on this daemon and the gh CLI isn't available here,")
	fmt.Println("  so paste a token instead. Create one at:")
	fmt.Println("    https://github.com/settings/tokens")
	fmt.Println("  A fine-grained token with Contents: Read and Write on the repos your islands")
	fmt.Println("  should reach covers cloning and pushing, and is tighter than the guided")
	fmt.Println("  flow's scopes. Add Workflows: Read and Write too if agents there edit")
	fmt.Println("  .github/workflows/ — GitHub refuses those pushes without it, and this")
	fmt.Println("  daemon cannot detect the difference on a fine-grained token.")
	fmt.Println()
	fmt.Print("Token (input hidden): ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("no token entered")
	}
	return tok, nil
}
