// Package selfupdate powers `dejima update`. Dejima installs two ways, so it
// updates two ways (the "dual-mode" epic):
//
//   - source mode  — a dev/server box with the repo checked out, installed via
//     `make install`. Updating means `git pull` + rebuild + reinstall.
//   - release mode — a client that installed a tagged binary (install.sh /
//     a release asset). Updating means fetching the latest release binary and
//     replacing the running one.
//
// The mode is inferred from the build version: a real semver release (stamped
// via -ldflags at `make release`) means release mode; "dev"/a git-describe
// string means a source checkout.
//
// This file is the *read-only* foundation: detect the mode and report whether a
// newer release exists. The mutating steps (git pull/rebuild; download/replace/
// restart) are layered on top deliberately, behind their own review.
package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/version"
	"strconv"
)

// githubToken returns a GitHub token from the environment, if any, used to lift
// the release-check off the unauthenticated 60/hr rate limit (→ 5000/hr).
func githubToken() string {
	for _, e := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(e)); v != "" {
			return v
		}
	}
	return ""
}

// Mode is how this install updates itself.
type Mode string

const (
	ModeSource  Mode = "source"  // repo checkout — git pull + make install
	ModeRelease Mode = "release" // tagged binary — fetch + replace the release asset
)

// DetectMode infers the install mode from the build version. A real semver
// release was produced by `make release` (a packaged client build); anything
// else ("dev", a git-describe string) is a working checkout.
func DetectMode() Mode {
	v := strings.TrimSpace(version.Version)
	// A *clean* release tag (vX.Y.Z, no suffix) is a packaged client build.
	// version.IsRelease accepts a git-describe string too (it ignores the
	// "-N-gHASH"/"-dirty" suffix), so additionally require no suffix — that's
	// what separates a release client from a `make`-from-checkout dev/server.
	if version.IsRelease(v) && !strings.ContainsAny(v, "-+") {
		return ModeRelease
	}
	return ModeSource
}

// releasesURL is the GitHub "latest release" endpoint, overridable in tests.
var releasesURL = "https://api.github.com/repos/aoos/dejima/releases/latest"

// LatestRelease returns the tag of the newest published (non-prerelease) release.
func LatestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := githubToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("query latest release: %w", err)
	}
	defer resp.Body.Close()
	// 403/429 from the GitHub API is usually the unauthenticated rate limit
	// (60/hr, shared per source IP), but not always — a bad GITHUB_TOKEN also
	// 403s, and telling that operator to "retry shortly" sends them to wait for
	// a limit that was never the problem. Distinguish on the header GitHub sets
	// specifically for exhaustion, and say when it clears so "shortly" isn't a
	// guess.
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return "", rateLimitError(resp)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github releases: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode release: %w", err)
	}
	if body.TagName == "" {
		return "", fmt.Errorf("no tag in latest release")
	}
	return body.TagName, nil
}

// rateLimitError turns a 403/429 into a message that says which of the two
// causes it was, and — when the limit really is exhausted — how long until it
// resets, so the caller isn't left guessing at "shortly".
func rateLimitError(resp *http.Response) error {
	remaining := strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining"))
	if remaining != "" && remaining != "0" {
		// Quota left, so exhaustion is not the cause — most likely a token that
		// is invalid, expired, or lacks access.
		if githubToken() != "" {
			return fmt.Errorf("github API refused the update check (HTTP %d) — your GITHUB_TOKEN looks invalid or expired; unset it to fall back to anonymous checks", resp.StatusCode)
		}
		return fmt.Errorf("github API refused the update check (HTTP %d)", resp.StatusCode)
	}

	wait := ""
	if reset := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			if d := time.Until(time.Unix(epoch, 0)); d > 0 {
				wait = fmt.Sprintf(" — resets in %s", d.Round(time.Minute))
			}
		}
	}
	if wait == "" {
		wait = " — retry shortly"
	}
	if githubToken() != "" {
		return fmt.Errorf("github API rate limit reached%s", wait)
	}
	// The anonymous limit is small and shared per source IP, so a busy network
	// can exhaust it without this machine doing anything unusual.
	return fmt.Errorf("github API rate limit reached for this network's IP address%s. "+
		"The daemon is fine; only the update CHECK is blocked. Set GITHUB_TOKEN to raise the limit", wait)
}

// Status is the result of an update check.
type Status struct {
	Current         string // this build's version
	Latest          string // newest published release tag
	Mode            Mode
	UpdateAvailable bool
}

// Evaluate compares the current version against the latest tag for the given
// mode. Pure (no I/O) so the decision is testable; Check wires it to the network.
func Evaluate(current, latest string, mode Mode) Status {
	return Status{
		Current:         current,
		Latest:          latest,
		Mode:            mode,
		UpdateAvailable: version.Compare(latest, current) > 0,
	}
}

// Check fetches the latest release and evaluates it against this build.
func Check(ctx context.Context) (Status, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	latest, err := LatestRelease(cctx)
	if err != nil {
		return Status{}, err
	}
	return Evaluate(version.Version, latest, DetectMode()), nil
}
