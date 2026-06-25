package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/githubid"
	"github.com/aoos/dejima/internal/hostterm"
	"github.com/aoos/dejima/internal/link"
	"github.com/aoos/dejima/internal/mailbox"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/providercreds"
)

// Client is a thin HTTP client for the Dejima API.
type Client struct {
	httpc *http.Client
	base  string
	// token, when set, is sent as an Authorization: Bearer header on every
	// request — the in-island autonomy path, where DEJIMA_TOKEN authenticates
	// the brain to the host-internal dejimad listener. Empty for the unix
	// socket and tailnet paths, which are trusted by other means.
	token string
}

// NewUnixClient returns a Client that talks to dejimad over its Unix socket.
func NewUnixClient() (*Client, error) {
	socket, err := paths.SocketPath()
	if err != nil {
		return nil, err
	}
	return &Client{
		httpc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socket)
				},
			},
		},
		// The host portion is ignored by the unix dialer but http.Client requires it.
		base: "http://dejimad",
	}, nil
}

// NewTCPClient returns a Client that talks to a remote dejimad over TCP.
// The host argument may be a bare "host:port" or a full URL.
func NewTCPClient(host string) (*Client, error) {
	base := host
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return &Client{
		httpc: &http.Client{Timeout: 30 * time.Second},
		base:  strings.TrimRight(base, "/"),
	}, nil
}

// NewTCPClientWithToken is NewTCPClient with a bearer token attached to every
// request: the in-island → dejimad autonomy path. host is typically
// DEJIMA_HOST (host.docker.internal:<port>) and token is DEJIMA_TOKEN, both
// injected into the container by the daemon when autonomy is enabled.
func NewTCPClientWithToken(host, token string) (*Client, error) {
	c, err := NewTCPClient(host)
	if err != nil {
		return nil, err
	}
	c.token = token
	return c, nil
}

// DaemonHost returns the hostname this client talks to (no scheme/port), or ""
// when the daemon is local (the unix-socket sentinel). Callers needing a
// reachable address for a *daemon-side* service — e.g. the SSH façade, whose
// bind the daemon may report host-less as ":2222" — use this to point at the
// daemon's host rather than the local machine's.
func (c *Client) DaemonHost() string {
	u, err := url.Parse(c.base)
	if err != nil {
		return ""
	}
	if h := u.Hostname(); h != "dejimad" { // "dejimad" is the unix-socket sentinel
		return h
	}
	return ""
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	return c.doVia(ctx, c.httpc, method, path, in, out)
}

// doLong is do() for operations whose legitimate duration can exceed the 30s
// default client timeout: island clone (copies two volumes via throwaway
// containers) and panic engage/clear (stops or restarts EVERY island in turn).
// http.Client.Timeout is a HARD cap that overrides the request context — so
// those callers' generous contexts (clone passes 5m) were silently truncated to
// 30s and the op failed mid-flight on a real host. This path uses a
// no-fixed-timeout client so the caller's context deadline is the only bound.
// Callers MUST pass a ctx that carries a deadline.
func (c *Client) doLong(ctx context.Context, method, path string, in, out any) error {
	return c.doVia(ctx, c.longHTTPClient(), method, path, in, out)
}

// longHTTPClient shares the transport (so the unix-socket dialer + connection
// pooling still apply) but carries NO fixed Timeout, leaving the request context
// as the sole deadline. See doLong.
func (c *Client) longHTTPClient() *http.Client {
	return &http.Client{Transport: c.httpc.Transport}
}

func (c *Client) doVia(ctx context.Context, hc *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		buf, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("daemon unreachable: %w (is dejimad running?)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var er ErrorResponse
		if json.NewDecoder(resp.Body).Decode(&er) == nil && er.Error != "" {
			return fmt.Errorf("%s", er.Error)
		}
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// PushClaudeCredentials stores a Claude credentials blob on the daemon host
// as the seed for new islands.
func (c *Client) PushClaudeCredentials(ctx context.Context, credentialsJSON []byte) error {
	req := PushCredentialsRequest{CredentialsJSON: string(credentialsJSON)}
	return c.do(ctx, http.MethodPut, "/v1/credentials/claude", req, nil)
}

// ClaudeCredentialsStatus reports whether the daemon can seed islands with
// Claude credentials, and from where.
func (c *Client) ClaudeCredentialsStatus(ctx context.Context) (*ClaudeCredentialsStatus, error) {
	var st ClaudeCredentialsStatus
	if err := c.do(ctx, http.MethodGet, "/v1/credentials/claude", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// ListGitHubIdentities returns the daemon's GitHub identities (no tokens).
func (c *Client) ListGitHubIdentities(ctx context.Context) ([]githubid.Meta, error) {
	var out GitHubIdentitiesResponse
	if err := c.do(ctx, http.MethodGet, "/v1/credentials/github", nil, &out); err != nil {
		return nil, err
	}
	return out.Identities, nil
}

// PutGitHubIdentity seeds or updates a named GitHub identity on the daemon.
func (c *Client) PutGitHubIdentity(ctx context.Context, name string, req PutGitHubIdentityRequest) ([]githubid.Meta, error) {
	var out GitHubIdentitiesResponse
	if err := c.do(ctx, http.MethodPut, "/v1/credentials/github/"+url.PathEscape(name), req, &out); err != nil {
		return nil, err
	}
	return out.Identities, nil
}

// DeleteGitHubIdentity removes a GitHub identity from the daemon and returns the
// names of any islands that still referenced it, so the caller can warn that
// those islands will lose this auth on their next reseed.
func (c *Client) DeleteGitHubIdentity(ctx context.Context, name string) (affected []string, err error) {
	var out DeleteGitHubIdentityResponse
	if err := c.do(ctx, http.MethodDelete, "/v1/credentials/github/"+url.PathEscape(name), nil, &out); err != nil {
		return nil, err
	}
	return out.AffectedIslands, nil
}

// ListProviderCredentials returns the daemon's LLM provider credentials without
// their keys (masked hint only).
func (c *Client) ListProviderCredentials(ctx context.Context) ([]providercreds.Meta, error) {
	var out ProviderCredentialsResponse
	if err := c.do(ctx, http.MethodGet, "/v1/credentials/providers", nil, &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

// PutProviderCredential stores or updates a provider key on the daemon.
func (c *Client) PutProviderCredential(ctx context.Context, provider string, req PutProviderCredentialRequest) ([]providercreds.Meta, error) {
	var out ProviderCredentialsResponse
	if err := c.do(ctx, http.MethodPut, "/v1/credentials/providers/"+url.PathEscape(provider), req, &out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

// DeleteProviderCredential removes a provider key and returns the names of any
// islands that still reference it.
func (c *Client) DeleteProviderCredential(ctx context.Context, provider string) (affected []string, err error) {
	var out DeleteProviderCredentialResponse
	if err := c.do(ctx, http.MethodDelete, "/v1/credentials/providers/"+url.PathEscape(provider), nil, &out); err != nil {
		return nil, err
	}
	return out.AffectedIslands, nil
}

// ListAgentTypes returns the capability descriptors for the built-in agent types.
func (c *Client) ListAgentTypes(ctx context.Context) ([]AgentTypeCapability, error) {
	var out AgentTypesResponse
	if err := c.do(ctx, http.MethodGet, "/v1/agent-types", nil, &out); err != nil {
		return nil, err
	}
	return out.Types, nil
}

// ConfigureAgent sets an agent's LLM provider/model.
func (c *Client) ConfigureAgent(ctx context.Context, island, id string, req AgentConfigRequest) (*AgentConfigResponse, error) {
	var out AgentConfigResponse
	path := "/v1/islands/" + url.PathEscape(island) + "/agents/" + url.PathEscape(id) + "/config"
	if err := c.do(ctx, http.MethodPatch, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendMailbox posts a message into an island's intra-island mailbox.
func (c *Client) SendMailbox(ctx context.Context, island string, req MailboxSendRequest) (*mailbox.Message, error) {
	var out mailbox.Message
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+url.PathEscape(island)+"/mailbox", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PollMailbox returns the messages in an island's mailbox visible to agent (or
// just broadcasts when agent is "") with seq > since.
func (c *Client) PollMailbox(ctx context.Context, island, agent string, since int64) (*MailboxPollResponse, error) {
	path := "/v1/islands/" + url.PathEscape(island) + "/mailbox"
	q := url.Values{}
	if agent != "" {
		q.Set("agent", agent)
	}
	if since > 0 {
		q.Set("since", strconv.FormatInt(since, 10))
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out MailboxPollResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GrantLink authorizes a directional inter-island info channel (operator-only).
func (c *Client) GrantLink(ctx context.Context, req LinkGrantRequest) (*link.Grant, error) {
	var out link.Grant
	if err := c.do(ctx, http.MethodPost, "/v1/links", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeLink drops a link grant (operator-only).
func (c *Client) RevokeLink(ctx context.Context, from, to, topic string) error {
	q := url.Values{"from": {from}, "to": {to}, "topic": {topic}}
	return c.do(ctx, http.MethodDelete, "/v1/links?"+q.Encode(), nil, nil)
}

// ListLinks returns every link grant (operator-only).
func (c *Client) ListLinks(ctx context.Context) ([]link.Grant, error) {
	var out LinksResponse
	if err := c.do(ctx, http.MethodGet, "/v1/links", nil, &out); err != nil {
		return nil, err
	}
	return out.Grants, nil
}

// SendLink sends an info message from island to a specific agent in another
// island over a granted channel; it's delivered into that agent's mailbox.
func (c *Client) SendLink(ctx context.Context, island string, req LinkSendRequest) (*mailbox.Message, error) {
	var out mailbox.Message
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+url.PathEscape(island)+"/link/send", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExposeAction adds a named action type to an island's exposed set (operator).
func (c *Client) ExposeAction(ctx context.Context, island, action string) ([]string, error) {
	var out LinkExposedResponse
	path := "/v1/islands/" + url.PathEscape(island) + "/link/actions/" + url.PathEscape(action)
	if err := c.do(ctx, http.MethodPut, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Actions, nil
}

// UnexposeAction removes an exposed action type (operator).
func (c *Client) UnexposeAction(ctx context.Context, island, action string) error {
	return c.do(ctx, http.MethodDelete, "/v1/islands/"+url.PathEscape(island)+"/link/actions/"+url.PathEscape(action), nil, nil)
}

// ListExposedActions returns an island's exposed action types.
func (c *Client) ListExposedActions(ctx context.Context, island string) ([]string, error) {
	var out LinkExposedResponse
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+url.PathEscape(island)+"/link/actions", nil, &out); err != nil {
		return nil, err
	}
	return out.Actions, nil
}

// RequestLinkAction asks another island to run a named action over a granted
// channel. The response says whether it executed (pre-authorized) or is pending
// operator approval.
func (c *Client) RequestLinkAction(ctx context.Context, island string, req LinkActionRequest) (*LinkActionResponse, error) {
	var out LinkActionResponse
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+url.PathEscape(island)+"/link/action", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListPendingActions returns the queued cross-island action approvals (operator).
func (c *Client) ListPendingActions(ctx context.Context) ([]link.ActionRequest, error) {
	var out LinkPendingResponse
	if err := c.do(ctx, http.MethodGet, "/v1/link/actions", nil, &out); err != nil {
		return nil, err
	}
	return out.Pending, nil
}

// ApproveAction approves and executes a pending action (operator).
func (c *Client) ApproveAction(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/link/actions/"+url.PathEscape(id)+"/approve", nil, nil)
}

// DenyAction denies a pending action (operator).
func (c *Client) DenyAction(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/link/actions/"+url.PathEscape(id)+"/deny", nil, nil)
}

// ListGitHubRepos lists repositories the identity can access, fetched daemon-side
// so a client without its own gh can still browse. capped is true when the
// identity sees more repos than the single page returned.
func (c *Client) ListGitHubRepos(ctx context.Context, name string) (repos []githubid.Repo, capped bool, err error) {
	var out GitHubReposResponse
	if err := c.do(ctx, http.MethodGet, "/v1/credentials/github/"+url.PathEscape(name)+"/repos", nil, &out); err != nil {
		return nil, false, err
	}
	return out.Repos, out.Capped, nil
}

// Health returns nil if dejimad is reachable and healthy.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/healthz", nil, nil)
}

// ListPortScopes returns an island's brokered host-file grants.
func (c *Client) ListPortScopes(ctx context.Context, name string) (*PortScopesResponse, error) {
	var out PortScopesResponse
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name+"/port/scopes", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GrantPortScope grants an island brokered access to a host directory.
func (c *Client) GrantPortScope(ctx context.Context, name, hostPath, mode string) (*PortScopeView, error) {
	var out PortScopeView
	req := PortScopeRequest{HostPath: hostPath, Mode: mode}
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/port/scopes", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokePortScope drops a grant by scope name.
func (c *Client) RevokePortScope(ctx context.Context, name, scope string) error {
	return c.do(ctx, http.MethodDelete, "/v1/islands/"+name+"/port/scopes/"+url.PathEscape(scope), nil, nil)
}

// ListCapabilityGrants returns the capability targets an island may invoke.
func (c *Client) ListCapabilityGrants(ctx context.Context, name string) (*CapabilityGrantsResponse, error) {
	var out CapabilityGrantsResponse
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name+"/capability/grants", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GrantCapability grants an island permission to invoke a named host capability.
func (c *Client) GrantCapability(ctx context.Context, name, target string) (*CapabilityGrantView, error) {
	var out CapabilityGrantView
	req := CapabilityGrantRequest{Target: target}
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/capability/grants", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RevokeCapability drops a capability grant by target name.
func (c *Client) RevokeCapability(ctx context.Context, name, target string) error {
	return c.do(ctx, http.MethodDelete, "/v1/islands/"+name+"/capability/grants/"+url.PathEscape(target), nil, nil)
}

// PortIntake brokers a host file (within a granted scope) into the island.
func (c *Client) PortIntake(ctx context.Context, name, scope, srcRel, dest string) (*PortIntakeResponse, error) {
	var out PortIntakeResponse
	req := PortIntakeRequest{Scope: scope, SrcRel: srcRel, Dest: dest}
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/port/intake", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AuditQuery are the optional filters for reading or exporting the ledger. The
// zero value reads the whole ledger. Verification always covers the full chain
// regardless of any filter — these only narrow what is returned.
type AuditQuery struct {
	Limit    int    // last N entries after filtering (0 = all)
	Island   string // exact island name
	Type     string // type prefix ("port" matches port.*) or an exact dotted type
	Actor    string // exact actor
	Decision string // "allowed" | "denied"
	Since    string // RFC3339 lower bound (inclusive)
	Until    string // RFC3339 upper bound (inclusive)
}

// values renders the query as URL parameters (omitting empty fields).
func (q AuditQuery) values() url.Values {
	v := url.Values{}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Island != "" {
		v.Set("island", q.Island)
	}
	if q.Type != "" {
		v.Set("type", q.Type)
	}
	if q.Actor != "" {
		v.Set("actor", q.Actor)
	}
	if q.Decision != "" {
		v.Set("decision", q.Decision)
	}
	if q.Since != "" {
		v.Set("since", q.Since)
	}
	if q.Until != "" {
		v.Set("until", q.Until)
	}
	return v
}

// Audit returns the Ledger (filtered per q) plus its whole-chain verification
// result.
func (c *Client) Audit(ctx context.Context, q AuditQuery) (*AuditResponse, error) {
	path := "/v1/audit"
	if vs := q.values(); len(vs) > 0 {
		path += "?" + vs.Encode()
	}
	var out AuditResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AuditExport streams the filtered ledger in the given format ("jsonl" or
// "csv"). The caller owns the returned reader and must Close it.
func (c *Client) AuditExport(ctx context.Context, q AuditQuery, format string) (io.ReadCloser, error) {
	vs := q.values()
	vs.Set("format", format)
	return c.getRaw(ctx, "/v1/audit?"+vs.Encode())
}

// getRaw performs a GET and returns the response body unparsed (for streamed
// exports). On an error status it drains the body into an error and closes it.
func (c *Client) getRaw(ctx context.Context, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon unreachable: %w (is dejimad running?)", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var er ErrorResponse
		if json.NewDecoder(resp.Body).Decode(&er) == nil && er.Error != "" {
			return nil, fmt.Errorf("%s", er.Error)
		}
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// ActivityFilter narrows the team activity feed. All fields are optional.
type ActivityFilter struct {
	Actor    string
	Island   string
	Owner    string
	Kind     string // lifecycle | broker | system
	Decision string // allowed | denied
	Since    string // RFC3339
	Until    string // RFC3339
	Limit    int
}

// Activity returns the curated team activity feed (newest-first), narrowed by the
// filter. AuditEnabled in the response reflects whether the full who-did-what log
// is on (vs only the always-on agent↔host broker records).
func (c *Client) Activity(ctx context.Context, f ActivityFilter) (*ActivityResponse, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	set("actor", f.Actor)
	set("island", f.Island)
	set("owner", f.Owner)
	set("kind", f.Kind)
	set("decision", f.Decision)
	set("since", f.Since)
	set("until", f.Until)
	if f.Limit > 0 {
		q.Set("limit", strconv.Itoa(f.Limit))
	}
	path := "/v1/activity"
	if e := q.Encode(); e != "" {
		path += "?" + e
	}
	var out ActivityResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PortExport copies a file out of the island into host-owned export staging.
func (c *Client) PortExport(ctx context.Context, name, src string) (*PortExportResponse, error) {
	var out PortExportResponse
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/port/export", PortExportRequest{Src: src}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PortWrite copies a file out of the island into a read-write host scope.
func (c *Client) PortWrite(ctx context.Context, name, scope, src, destRel string) (*PortWriteResponse, error) {
	var out PortWriteResponse
	req := PortWriteRequest{Scope: scope, Src: src, DestRel: destRel}
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/port/write", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListIslands returns every island known to the daemon.
func (c *Client) ListIslands(ctx context.Context) ([]IslandInfo, error) {
	var out []IslandInfo
	if err := c.do(ctx, http.MethodGet, "/v1/islands", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetIsland returns one island's info.
func (c *Client) GetIsland(ctx context.Context, name string) (*IslandInfo, error) {
	var out IslandInfo
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WorkspaceReady reports whether the island's repo clone has landed in
// /workspace yet. Used by `dejima connect` to wait out provisioning.
func (c *Client) WorkspaceReady(ctx context.Context, name string) (bool, error) {
	var out WorkspaceReadyResponse
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name+"/workspace-ready", nil, &out); err != nil {
		return false, err
	}
	return out.Ready, nil
}

// CreateIsland provisions a new island. The returned CreateIslandResponse
// embeds the IslandInfo; on a token-authenticated create by a Home Island it
// also carries the child's bearer Token (the parent-child spawn model).
func (c *Client) CreateIsland(ctx context.Context, req CreateIslandRequest) (*CreateIslandResponse, error) {
	var out CreateIslandResponse
	if err := c.do(ctx, http.MethodPost, "/v1/islands", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteIsland tears down an island (purge). When force is false the daemon
// refuses if the workspace has uncommitted or unpushed git work; force bypasses
// that guard.
func (c *Client) DeleteIsland(ctx context.Context, name string, force bool) error {
	path := "/v1/islands/" + name
	if force {
		path += "?force=true"
	}
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// Panic engages the daemon-wide panic stop: every island is stopped and a
// PANIC flag is written so the daemon won't auto-start them on restart.
func (c *Client) Panic(ctx context.Context, reason string) (*PanicResponse, error) {
	var out PanicResponse
	// doLong: panic stops every island in turn, which can exceed the 30s default.
	if err := c.doLong(ctx, http.MethodPost, "/v1/panic", PanicRequest{Reason: reason}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ClearPanic removes the PANIC flag and restarts islands whose desired state is
// running.
func (c *Client) ClearPanic(ctx context.Context) (*PanicResponse, error) {
	var out PanicResponse
	// doLong: clearing restarts every running island in turn — same slow class.
	if err := c.doLong(ctx, http.MethodDelete, "/v1/panic", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PanicStatus reports whether panic mode is currently engaged.
func (c *Client) PanicStatus(ctx context.Context) (*PanicResponse, error) {
	var out PanicResponse
	if err := c.do(ctx, http.MethodGet, "/v1/panic", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CloneIsland duplicates an island under newName, copying its workspace + home
// volumes (credentials and git history come along).
func (c *Client) CloneIsland(ctx context.Context, name, newName string) (*IslandInfo, error) {
	var out IslandInfo
	// doLong: copies two volumes via throwaway containers — routinely > 30s.
	if err := c.doLong(ctx, http.MethodPost, "/v1/islands/"+name+"/clone", CloneIslandRequest{NewName: newName}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// HibernateIsland stops the container, preserving volumes.
func (c *Client) HibernateIsland(ctx context.Context, name string) (*IslandInfo, error) {
	var out IslandInfo
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/hibernate", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// WakeIsland starts a hibernated island.
func (c *Client) WakeIsland(ctx context.Context, name string) (*IslandInfo, error) {
	var out IslandInfo
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/wake", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpgradeIsland recreates an island's container against the current island
// image, preserving both workspace and agent state.
func (c *Client) UpgradeIsland(ctx context.Context, name string) (*IslandInfo, error) {
	var info IslandInfo
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/upgrade", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// BuildImage asks the daemon to rebuild the island image from its embedded
// build context, copying build output to out. Returns nil only when the
// daemon confirms the build succeeded.
func (c *Client) BuildImage(ctx context.Context, out io.Writer) error {
	body, err := c.stream(ctx, http.MethodPost, "/v1/image/build")
	if err != nil {
		return err
	}
	defer body.Close()

	// The build's exit status arrives in-stream (the 200 header is long gone
	// by then): a success marker line, or a trailing "ERROR: …" line.
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	last := ""
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == imageBuildOKMarker {
			return nil
		}
		if strings.TrimSpace(line) != "" {
			last = line
		}
		fmt.Fprintln(out, line)
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("build stream interrupted: %w", err)
	}
	if strings.HasPrefix(last, "ERROR: ") {
		return errors.New(strings.TrimPrefix(last, "ERROR: "))
	}
	return errors.New("build stream ended without a result (daemon restarted?)")
}

// stream issues a request whose response body may outlive the standard client
// timeout (followed logs, image builds). Cancellation still flows through ctx.
func (c *Client) stream(ctx context.Context, method, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	sc := &http.Client{Transport: c.httpc.Transport}
	resp, err := sc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("daemon unreachable: %w (is dejimad running?)", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, decodeErr(resp)
	}
	return resp.Body, nil
}

// ResetIsland clears agent state, preserves workspace.
func (c *Client) ResetIsland(ctx context.Context, name string) (*IslandInfo, error) {
	var out IslandInfo
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/reset", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetIslandTitle sets an island's cosmetic display title (empty clears it).
func (c *Client) SetIslandTitle(ctx context.Context, name, title string) (*IslandInfo, error) {
	var out IslandInfo
	if err := c.do(ctx, http.MethodPatch, "/v1/islands/"+name, UpdateIslandRequest{Title: title}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ExecInIsland runs a one-shot command inside an island and returns its output.
func (c *Client) ExecInIsland(ctx context.Context, name string, cmd []string) (*ExecResponse, error) {
	var out ExecResponse
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/exec", ExecRequest{Cmd: cmd}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReadFile streams a file out of the island.
func (c *Client) ReadFile(ctx context.Context, name, path string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/islands/"+name+"/files/"+strings.TrimPrefix(path, "/"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, decodeErr(resp)
	}
	return resp.Body, nil
}

// WriteFile uploads body into a file inside the island.
func (c *Client) WriteFile(ctx context.Context, name, path string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+"/v1/islands/"+name+"/files/"+strings.TrimPrefix(path, "/"), body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return decodeErr(resp)
	}
	return nil
}

// StreamLogs returns a reader yielding the container's logs. follow keeps the
// stream open until ctx is canceled. Uses the timeout-free stream path so a
// followed stream isn't cut off by the standard 30s client timeout.
func (c *Client) StreamLogs(ctx context.Context, name, agentID string, follow bool) (io.ReadCloser, error) {
	q := url.Values{}
	if follow {
		q.Set("follow", "true")
	}
	if agentID != "" {
		q.Set("agent", agentID)
	}
	path := "/v1/islands/" + name + "/logs"
	if e := q.Encode(); e != "" {
		path += "?" + e
	}
	return c.stream(ctx, http.MethodGet, path)
}

func decodeErr(resp *http.Response) error {
	var er ErrorResponse
	if json.NewDecoder(resp.Body).Decode(&er) == nil && er.Error != "" {
		return fmt.Errorf("%s", er.Error)
	}
	return fmt.Errorf("http %d", resp.StatusCode)
}

// SubscribeWebhook registers a webhook URL with the daemon.
func (c *Client) SubscribeWebhook(ctx context.Context, url, secret string, eventTypes []events.Type) (*events.Subscription, error) {
	req := SubscribeWebhookRequest{URL: url, Secret: secret, Events: eventTypes}
	var out events.Subscription
	if err := c.do(ctx, http.MethodPost, "/v1/events/subscribe", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListWebhooks returns every webhook subscription.
func (c *Client) ListWebhooks(ctx context.Context) ([]events.Subscription, error) {
	var out []events.Subscription
	if err := c.do(ctx, http.MethodGet, "/v1/events/subscriptions", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UnsubscribeWebhook removes a webhook subscription by ID.
func (c *Client) UnsubscribeWebhook(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/events/subscriptions/"+id, nil, nil)
}

// RevokeAllSessions drops every active client websocket. Returns the count.
func (c *Client) RevokeAllSessions(ctx context.Context) (int, error) {
	var out struct {
		Revoked int `json:"revoked"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/sessions/revoke", nil, &out); err != nil {
		return 0, err
	}
	return out.Revoked, nil
}

// ClientHistory returns the daemon's in-memory attach/detach history.
func (c *Client) ClientHistory(ctx context.Context) ([]ClientHistoryEntry, error) {
	var out []ClientHistoryEntry
	if err := c.do(ctx, http.MethodGet, "/v1/clients", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Overview returns server-wide aggregates.
func (c *Client) Overview(ctx context.Context) (*OverviewResponse, error) {
	var out OverviewResponse
	if err := c.do(ctx, http.MethodGet, "/v1/overview", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AuthorizeAccountKey enrolls a public key fleet-wide via the daemon (which
// performs the write). Lets any operator device self-enroll without copying its
// key to the daemon host. Returns the key's fingerprint.
func (c *Client) AuthorizeAccountKey(ctx context.Context, publicKey string) (string, error) {
	var out AuthorizeSSHKeyResponse
	if err := c.do(ctx, http.MethodPost, "/v1/ssh/account-keys", AuthorizeSSHKeyRequest{PublicKey: publicKey}, &out); err != nil {
		return "", err
	}
	return out.Fingerprint, nil
}

// ListAccountKeys returns the fleet-wide authorized SSH keys.
func (c *Client) ListAccountKeys(ctx context.Context) ([]SSHKeyInfo, error) {
	var out ListSSHKeysResponse
	if err := c.do(ctx, http.MethodGet, "/v1/ssh/account-keys", nil, &out); err != nil {
		return nil, err
	}
	return out.Keys, nil
}

// DaemonUpdate asks the daemon to update itself. execute=false reports the plan;
// execute=true applies it (the daemon then restarts, so the connection may drop
// right after this returns — the Applying flag is the confirmation it began).
// With execute and clients attached, the daemon defers (Deferred=true) unless
// force is set, which applies anyway and drops those sessions.
func (c *Client) DaemonUpdate(ctx context.Context, execute, force bool) (*AdminUpdateResponse, error) {
	var out AdminUpdateResponse
	if err := c.do(ctx, http.MethodPost, "/v1/admin/update", AdminUpdateRequest{Execute: execute, Force: force}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateIslandResources sets an island's memory limit and/or OOM priority.
// Memory applies live; an OOM-priority change reports RestartRequired (it takes
// effect on the next container recreate).
func (c *Client) UpdateIslandResources(ctx context.Context, name string, req UpdateResourcesRequest) (*UpdateResourcesResponse, error) {
	var out UpdateResourcesResponse
	if err := c.do(ctx, http.MethodPut, "/v1/islands/"+name+"/resources", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IslandEvents returns the recent event log for one island.
func (c *Client) IslandEvents(ctx context.Context, name string) ([]events.Event, error) {
	var out []events.Event
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name+"/events", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DialSession opens a websocket against the island's primary-agent session.
// ErrSessionGone tags a websocket-attach failure where the daemon positively
// reported the session/island is gone (a 404/410 handshake response), as opposed
// to a transient transport failure (daemon unreachable: laptop sleep, network
// drop, daemon restart). The reconnect loop uses it to stop retrying promptly —
// a gone session never comes back — while retrying transport failures for as
// long as the user keeps the terminal open, since the tmux session survives on
// the daemon.
var ErrSessionGone = errors.New("session gone")

// dialErr wraps a websocket.Dial failure. When the handshake got a positive
// gone response (404/410), the error wraps ErrSessionGone; otherwise it's a
// transport failure and the original error passes through. resp may be nil
// (pure transport error — no handshake response at all).
func dialErr(what string, resp *http.Response, err error) error {
	if resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) {
		return fmt.Errorf("%s: %w (http %d)", what, ErrSessionGone, resp.StatusCode)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (c *Client) DialSession(ctx context.Context, name, label string) (*websocket.Conn, error) {
	return c.DialAgentSession(ctx, name, "", label)
}

// DialAgentSession opens a websocket against a specific agent's session. An
// empty agentID targets the island's primary agent (the legacy route).
func (c *Client) DialAgentSession(ctx context.Context, name, agentID, label string) (*websocket.Conn, error) {
	q := url.Values{}
	if label != "" {
		q.Set("label", label)
	}
	// websocket.Dial requires ws:// or wss://; our http.Client transport still
	// handles the actual Unix-socket dial via DialContext.
	wsBase := c.base
	if len(wsBase) > 4 && wsBase[:4] == "http" {
		wsBase = "ws" + wsBase[4:]
	}
	path := "/v1/islands/" + name + "/session"
	if agentID != "" {
		path = "/v1/islands/" + name + "/agents/" + agentID + "/session"
	}
	wsURL := wsBase + path
	if encoded := q.Encode(); encoded != "" {
		wsURL += "?" + encoded
	}
	conn, resp, err := websocket.Dial(ctx, wsURL, c.wsDialOptions())
	if err != nil {
		return nil, dialErr("dial session", resp, err)
	}
	return conn, nil
}

// DialIslandShell opens a websocket against an island's in-island shell — a
// contained interactive bash session at /workspace inside the container. Shared
// and resumable; not tied to an agent.
func (c *Client) DialIslandShell(ctx context.Context, name, label string) (*websocket.Conn, error) {
	q := url.Values{}
	if label != "" {
		q.Set("label", label)
	}
	wsBase := c.base
	if len(wsBase) > 4 && wsBase[:4] == "http" {
		wsBase = "ws" + wsBase[4:]
	}
	wsURL := wsBase + "/v1/islands/" + name + "/shell/session"
	if encoded := q.Encode(); encoded != "" {
		wsURL += "?" + encoded
	}
	conn, _, err := websocket.Dial(ctx, wsURL, c.wsDialOptions())
	if err != nil {
		return nil, fmt.Errorf("dial island shell: %w", err)
	}
	return conn, nil
}

// wsDialOptions builds the websocket dial options, carrying the bearer token in
// an Authorization header when set. Without this, a token-authenticated client's
// session/terminal attach would arrive header-less and be treated by roleAuth as
// the trusted no-token (owner) caller — silently escalating a viewer/operator
// token. The unix-socket and tailnet paths leave token empty and rely on the
// listener's own trust.
func (c *Client) wsDialOptions() *websocket.DialOptions {
	opts := &websocket.DialOptions{HTTPClient: c.httpc}
	if c.token != "" {
		opts.HTTPHeader = http.Header{"Authorization": []string{"Bearer " + c.token}}
	}
	return opts
}

// ListTerminals returns the daemon's host terminals.
func (c *Client) ListTerminals(ctx context.Context) ([]hostterm.Terminal, error) {
	var out TerminalsResponse
	if err := c.do(ctx, http.MethodGet, "/v1/terminals", nil, &out); err != nil {
		return nil, err
	}
	return out.Terminals, nil
}

// CreateTerminal creates a host terminal with an optional label.
func (c *Client) CreateTerminal(ctx context.Context, label string) (*hostterm.Terminal, error) {
	var out hostterm.Terminal
	if err := c.do(ctx, http.MethodPost, "/v1/terminals", CreateTerminalRequest{Label: label}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteTerminal removes a host terminal (and kills its tmux session).
func (c *Client) DeleteTerminal(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/terminals/"+url.PathEscape(id), nil, nil)
}

// RelabelTerminal renames a host terminal.
func (c *Client) RelabelTerminal(ctx context.Context, id, label string) (*hostterm.Terminal, error) {
	var out hostterm.Terminal
	if err := c.do(ctx, http.MethodPatch, "/v1/terminals/"+url.PathEscape(id), RelabelTerminalRequest{Label: label}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DialTerminalSession opens a websocket against a host terminal's session.
func (c *Client) DialTerminalSession(ctx context.Context, id, label string) (*websocket.Conn, error) {
	q := url.Values{}
	if label != "" {
		q.Set("label", label)
	}
	wsBase := c.base
	if len(wsBase) > 4 && wsBase[:4] == "http" {
		wsBase = "ws" + wsBase[4:]
	}
	wsURL := wsBase + "/v1/terminals/" + url.PathEscape(id) + "/session"
	if encoded := q.Encode(); encoded != "" {
		wsURL += "?" + encoded
	}
	conn, resp, err := websocket.Dial(ctx, wsURL, c.wsDialOptions())
	if err != nil {
		return nil, dialErr("dial terminal session", resp, err)
	}
	return conn, nil
}

// ListAgents returns the agents in an island.
func (c *Client) ListAgents(ctx context.Context, name string) ([]AgentInfo, error) {
	var out []AgentInfo
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name+"/agents", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MoveAgent reorders an agent within its island by delta positions (negative =
// toward the front), clamped to the ends. Order is cosmetic.
func (c *Client) MoveAgent(ctx context.Context, name, id string, delta int) error {
	return c.do(ctx, http.MethodPost, "/v1/islands/"+url.PathEscape(name)+"/agents/"+url.PathEscape(id)+"/move", MoveAgentRequest{Delta: delta}, nil)
}

// AddAgent adds an agent to an island.
func (c *Client) AddAgent(ctx context.Context, name string, req AgentSpecRequest) (*AgentInfo, error) {
	var out AgentInfo
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/agents", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RemoveAgent removes an agent from an island by id.
func (c *Client) RemoveAgent(ctx context.Context, name, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/islands/"+name+"/agents/"+id, nil, nil)
}

// RelabelAgent sets an agent's cosmetic label (its id and type are immutable).
func (c *Client) RelabelAgent(ctx context.Context, name, id, label string) (*AgentInfo, error) {
	var out AgentInfo
	if err := c.do(ctx, http.MethodPatch, "/v1/islands/"+name+"/agents/"+id, AgentSpecRequest{Label: label}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateToken issues an operator bearer token with the given role and optional
// island scope. The returned CreateTokenResponse carries the bearer Secret —
// shown exactly once; the daemon stores only its hash.
func (c *Client) CreateToken(ctx context.Context, req CreateTokenRequest) (*CreateTokenResponse, error) {
	var out CreateTokenResponse
	if err := c.do(ctx, http.MethodPost, "/v1/tokens", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTokens returns every issued operator token's metadata (never a secret).
func (c *Client) ListTokens(ctx context.Context) ([]TokenView, error) {
	var out TokensResponse
	if err := c.do(ctx, http.MethodGet, "/v1/tokens", nil, &out); err != nil {
		return nil, err
	}
	return out.Tokens, nil
}

// RevokeToken deletes an operator token by id.
func (c *Client) RevokeToken(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/tokens/"+url.PathEscape(id), nil, nil)
}
