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

// VerifyToken confirms a token authenticates against host and returns the login,
// numeric user id, and SCOPES it carries. Called before storing an identity so a
// bad or expired token fails fast at `auth push` time instead of silently at
// clone/push time inside an island. The id feeds the canonical noreply commit
// email (see GitAuthor).
//
// scopes comes from the X-OAuth-Scopes header GitHub returns on this very call —
// it was always in the response and always discarded. Without it "your token
// authenticates" was the strongest thing any surface could say, so a token that
// could clone and push but NOT open a pull request looked identical to a working
// one until an agent hit the wall: "Resource not accessible by personal access
// token", naming nothing that could be acted on.
func VerifyToken(ctx context.Context, host, token string) (login string, id int64, scopes string, err error) {
	return verifyToken(ctx, apiBase(host), token)
}

func verifyToken(ctx context.Context, base, token string) (login string, id int64, scopes string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return "", 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", 0, "", fmt.Errorf("github api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var u struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", 0, "", fmt.Errorf("decode github user: %w", err)
	}
	// A FINE-GRAINED token sends no X-OAuth-Scopes header at all — its permissions
	// are per-repository and not expressible here. That is a different fact from
	// "this token has no scopes", and the two must not render the same: one is a
	// modern token we cannot introspect, the other is a classic token that can do
	// nothing. Callers get "" and are expected to say so.
	return u.Login, u.ID, strings.TrimSpace(resp.Header.Get("X-OAuth-Scopes")), nil
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

// NewRepo describes a repository to create. Visibility is an explicit bool with
// no default worth relying on: the zero value is a PUBLIC repo, and a caller
// that forgets to set it publishes someone's code. Every path to this struct is
// expected to make the operator choose, and CreateRepo will not guess.
type NewRepo struct {
	Name        string
	Description string
	Private     bool
}

// ValidateRepoName rejects names GitHub will reject, before the network call.
//
// Not politeness: the failure it prevents is a 422 whose body says "name is
// invalid" without saying which character did it, arriving after the operator
// has already confirmed a create. GitHub accepts ASCII letters, digits, '.',
// '-' and '_', and silently rewrites anything else — so a name that "worked"
// can come back as a repo the island then cannot find under the name that was
// typed.
func ValidateRepoName(name string) error {
	n := strings.TrimSpace(name)
	switch {
	case n == "":
		return fmt.Errorf("repository name is empty")
	case len(n) > 100:
		return fmt.Errorf("repository name is longer than GitHub's 100-character limit")
	case n == "." || n == "..":
		return fmt.Errorf("%q is not a usable repository name", n)
	}
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf("repository name may use only letters, digits, '.', '-' and '_' — %q is not allowed", string(r))
		}
	}
	return nil
}

// CreateRepo creates a repository owned by the identity's own account and
// returns it in the same shape ListRepos yields, so the island creator consumes
// a new repo and an existing one through one path.
//
// SCOPE IS CHECKED FIRST, and the check is the point. A classic token without
// `repo` authenticates perfectly and cannot create anything; GitHub answers that
// with a 403 whose body talks about resources, not scopes. That is the exact
// shape ScopeNote was written for — "your token authenticates" and "your token
// can do the work" are different questions, and only asking the first is how an
// operator reaches an island that cannot push. A fine-grained token reports no
// scopes at all, which is unknowable rather than absent, so it is allowed
// through to GitHub for the real answer.
//
// AUTO-INIT IS ON, deliberately. A repository created bare has no commit and no
// default branch: cloning it warns, leaves an island with no HEAD, and the first
// `git switch -c work origin/main` an agent runs fails against a ref that does
// not exist. One commit costs nothing and makes the island's first git operation
// behave like every other island's.
func CreateRepo(ctx context.Context, id Identity, spec NewRepo) (Repo, error) {
	if err := ValidateRepoName(spec.Name); err != nil {
		return Repo{}, err
	}
	if note, canWrite := ScopeNote(id.Scopes); !canWrite {
		return Repo{}, fmt.Errorf(
			"the %q identity (%s) cannot create repositories: its token has %s.\n"+
				"Re-connect it with the `repo` scope — `dejima auth push --github` from a machine "+
				"where `gh auth login` granted it, or issue a new token — then try again",
			id.Name, id.Login, note)
	}
	return createRepo(ctx, apiBase(id.Host), id.Token, spec)
}

func createRepo(ctx context.Context, base, token string, spec NewRepo) (Repo, error) {
	body, err := json.Marshal(map[string]any{
		"name":        strings.TrimSpace(spec.Name),
		"description": spec.Description,
		"private":     spec.Private,
		"auto_init":   true,
	})
	if err != nil {
		return Repo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/user/repos", strings.NewReader(string(body)))
	if err != nil {
		return Repo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return Repo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Repo{}, createError(resp.StatusCode, resp.Status, raw)
	}
	var r struct {
		FullName    string `json:"full_name"`
		CloneURL    string `json:"clone_url"`
		Description string `json:"description"`
		Private     bool   `json:"private"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Repo{}, fmt.Errorf("decode created repo: %w", err)
	}
	// GitHub is the authority on what was actually made — it may have adjusted the
	// name, and an org policy can force visibility. Returning ITS answer rather
	// than echoing the request is what stops the island being created against a
	// URL that does not exist, and what lets the caller notice a repo that came
	// back public when private was asked for.
	return Repo{NameWithOwner: r.FullName, URL: r.CloneURL, Description: r.Description, Private: r.Private}, nil
}

// createError turns GitHub's create failures into something an operator can act
// on. The 422 case matters most: "name already exists on this account" is by far
// the likeliest failure here and GitHub buries it in a nested errors array,
// which surfaced as a bare "422 Unprocessable Entity" — true, and useless.
func createError(code int, status string, raw []byte) error {
	var parsed struct {
		Message string `json:"message"`
		Errors  []struct {
			Field   string `json:"field"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &parsed)
	for _, e := range parsed.Errors {
		if strings.TrimSpace(e.Message) != "" {
			return fmt.Errorf("github refused to create the repository: %s", strings.TrimSpace(e.Message))
		}
	}
	if code == http.StatusForbidden || code == http.StatusUnauthorized {
		return fmt.Errorf("github refused to create the repository (%s): the token may lack the `repo` scope or SSO authorization", status)
	}
	if msg := strings.TrimSpace(parsed.Message); msg != "" {
		return fmt.Errorf("github refused to create the repository: %s", msg)
	}
	return fmt.Errorf("github api %s: %s", status, strings.TrimSpace(string(raw)))
}
