package reposrc

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// GHRepo is one repository as returned by `gh repo list`. URL is the https clone
// URL — handed to the daemon as the remote, cloned inside the island using the
// gh/git credentials already mounted there.
type GHRepo struct {
	NameWithOwner string `json:"nameWithOwner"`
	URL           string `json:"url"`
	Description   string `json:"description"`
	IsPrivate     bool   `json:"isPrivate"`
}

// GitHubAvailable reports whether the gh CLI is installed and authenticated on
// the client. It's the precondition for ListGitHub; the caller uses the error
// to fall back to manual URL entry with a helpful hint.
func GitHubAvailable() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found — install it, or paste a repo URL instead")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "gh", "auth", "status").CombinedOutput(); err != nil {
		return fmt.Errorf("gh not authenticated — run `gh auth login`, or paste a repo URL instead (%s)",
			firstLine(string(out)))
	}
	return nil
}

// ListGitHub returns the authenticated user's GitHub repositories via
// `gh repo list`, most-recently-pushed first, capped at limit (default 100).
// The listing uses the *client's* gh account; cloning later uses the daemon
// host's git credentials — on a single-host setup these are the same account.
func ListGitHub(limit int) ([]GHRepo, error) {
	if err := GitHubAvailable(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// gh repo list defaults to pushed-descending order, which is what we want.
	out, err := exec.CommandContext(ctx, "gh", "repo", "list",
		"--limit", strconv.Itoa(limit),
		"--json", "nameWithOwner,url,description,isPrivate").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh repo list failed: %s", firstLine(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("gh repo list failed: %w", err)
	}
	var repos []GHRepo
	if err := json.Unmarshal(out, &repos); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	return repos, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
