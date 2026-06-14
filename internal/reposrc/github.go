package reposrc

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// GitHubAvailable reports whether the gh CLI is installed and authenticated on
// this client. It's the precondition for reading the local identity to seed the
// daemon (LocalGitHubLogin); repo browsing itself is daemon-side.
func GitHubAvailable() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found — install it, or use `gh auth login` on the daemon host")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "gh", "auth", "status").CombinedOutput(); err != nil {
		return fmt.Errorf("gh not authenticated — run `gh auth login` (%s)", firstLine(string(out)))
	}
	return nil
}

// LocalGitHubLogin returns the active gh account's login and token on this
// client — used to seed a daemon GitHub identity via `dejima auth push
// --github`. Switch accounts with `gh auth switch` first to push the other one.
func LocalGitHubLogin() (login, token string, err error) {
	if err := GitHubAvailable(); err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tokOut, err := exec.CommandContext(ctx, "gh", "auth", "token").Output()
	if err != nil {
		return "", "", fmt.Errorf("gh auth token: %w", ghExitErr(err))
	}
	loginOut, err := exec.CommandContext(ctx, "gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		return "", "", fmt.Errorf("gh api user: %w", ghExitErr(err))
	}
	login = strings.TrimSpace(string(loginOut))
	token = strings.TrimSpace(string(tokOut))
	if login == "" || token == "" {
		return "", "", fmt.Errorf("gh returned an empty login or token")
	}
	return login, token, nil
}

func ghExitErr(err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		if line := firstLine(string(ee.Stderr)); line != "" {
			return fmt.Errorf("%s", line)
		}
	}
	return err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
