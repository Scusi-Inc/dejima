package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/reposrc"
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
func connectGitHubViaToken(ctx context.Context, c *api.Client, name string, makeDefault, shared bool) error {
	if err := reposrc.GitHubAvailable(); err != nil {
		return fmt.Errorf("guided GitHub sign-in isn't available on this daemon, and the token path needs the gh CLI here.\n\n"+
			"  1. Install and sign in to gh on this machine:\n"+
			"       gh auth login\n"+
			"  2. Re-run:\n"+
			"       dejima github connect\n\n"+
			"(%w)", err)
	}

	login, token, err := reposrc.LocalGitHubLogin("")
	if err != nil {
		return err
	}
	// Verify before storing, so a stale or over-narrow token fails here rather
	// than inside an island days later.
	verified, ghUserID, err := githubid.VerifyToken(ctx, "", token)
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
		Login: login, ID: ghUserID, Token: token, Default: makeDefault, Shared: shared,
	}); err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("connected GitHub identity %q (login %s) using your local gh session\n", id, login)
	fmt.Println("guided sign-in isn't configured on this daemon, so the token path was used instead —")
	fmt.Println("same result: islands can now clone and push as this identity.")
	fmt.Println()
	fmt.Println("check it with: dejima auth status")
	return nil
}
