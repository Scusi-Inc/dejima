package githubid

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Repo is one repository visible to an identity, in the shape the island
// creator consumes: a full owner/name and the https clone URL.
type Repo struct {
	NameWithOwner string `json:"name_with_owner"`
	URL           string `json:"url"` // https clone URL
	Description   string `json:"description"`
	Private       bool   `json:"private"`
}

// RepoList is a page of repositories plus whether the identity can see more
// than the page returned (the GitHub API advertises a next page via a Link
// header). Capped lets the UI say "showing the first N" honestly.
type RepoList struct {
	Repos  []Repo `json:"repos"`
	Capped bool   `json:"capped"`
}

// apiBase returns the REST API root for a GitHub host: api.github.com for the
// public host, /api/v3 for a GitHub Enterprise host.
func apiBase(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || host == DefaultHost {
		return "https://api.github.com"
	}
	return "https://" + host + "/api/v3"
}

// ListRepos returns repositories the identity can access (owner, collaborator,
// or org member), most-recently-pushed first, capped at limit (default/max 100,
// a single API page). The daemon owns this call so any client device — even one
// without gh — can browse before an island exists.
func ListRepos(ctx context.Context, id Identity, limit int) (RepoList, error) {
	return listRepos(ctx, apiBase(id.Host), id.Token, limit)
}

// VerifyToken confirms a token authenticates against host and returns the login
// and numeric user id it belongs to. Called before storing an identity so a bad
// or expired token fails fast at `auth push` time instead of silently at
// clone/push time inside an island. The id feeds the canonical noreply commit
// email (see GitAuthor).
func VerifyToken(ctx context.Context, host, token string) (login string, id int64, err error) {
	return verifyToken(ctx, apiBase(host), token)
}

func verifyToken(ctx context.Context, base, token string) (login string, id int64, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", 0, fmt.Errorf("github api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var u struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", 0, fmt.Errorf("decode github user: %w", err)
	}
	return u.Login, u.ID, nil
}

func listRepos(ctx context.Context, base, token string, limit int) (RepoList, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(limit))
	q.Set("sort", "pushed")
	q.Set("affiliation", "owner,collaborator,organization_member")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user/repos?"+q.Encode(), nil)
	if err != nil {
		return RepoList{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return RepoList{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return RepoList{}, fmt.Errorf("github api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var raw []struct {
		FullName    string `json:"full_name"`
		CloneURL    string `json:"clone_url"`
		Description string `json:"description"`
		Private     bool   `json:"private"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return RepoList{}, fmt.Errorf("decode github repos: %w", err)
	}
	out := make([]Repo, len(raw))
	for i, r := range raw {
		out[i] = Repo{NameWithOwner: r.FullName, URL: r.CloneURL, Description: r.Description, Private: r.Private}
	}
	// GitHub advertises further pages with a Link header carrying rel="next".
	// Its presence means the identity sees more repos than this single page.
	capped := strings.Contains(resp.Header.Get("Link"), `rel="next"`)
	return RepoList{Repos: out, Capped: capped}, nil
}
