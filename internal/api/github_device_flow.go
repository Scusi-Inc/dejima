package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/githubid"
)

// GitHub device-flow credential capture — the guided "connect GitHub" path (like
// `gh auth login`): the member sees a short code, enters it at
// github.com/login/device, and the daemon captures the resulting token and stores
// it as the member's OWN tenant identity. Containment: the OAuth token and the
// device_code (which exchanges for it) NEVER leave the daemon — the client only
// ever holds the opaque session id + the human user_code. The captured identity
// flows through the SAME ResolveForIsland chokepoint as a pasted PAT.
//
// A paste-a-PAT path (PUT /v1/credentials/github/{name}) remains the fallback,
// and a fine-grained per-repo PAT is the least-privilege option we keep prominent.

// deviceScopes is what a captured token can do: clone AND push private repos
// (agents commit) + read org membership (members often work on org repos). The
// grant is surfaced to the user in the flow — never a silent broad grant.
const deviceScopes = "repo read:org"

// githubDeviceCodeURL / githubTokenURL / githubUserURL are the GitHub endpoints
// the daemon calls; vars so tests can point them at a stub server.
var (
	githubDeviceCodeURL = "https://github.com/login/device/code"
	githubTokenURL      = "https://github.com/login/oauth/access_token"
	githubUserURL       = "https://api.github.com/user"
)

// deviceSession is a server-side pending device-flow authorization. The
// device_code is the secret that exchanges for a token — it stays here, keyed by
// an opaque session id handed to the client. Bound to the tenant that started it.
type deviceSession struct {
	deviceCode string
	owner      string // ghOwner tenant that started the flow — only they may poll it
	expiresAt  time.Time
	interval   int // GitHub's minimum seconds between polls
}

// deviceSessions is the daemon's bounded in-memory set of pending flows.
type deviceSessions struct {
	mu sync.Mutex
	m  map[string]deviceSession
}

func newDeviceSessions() *deviceSessions { return &deviceSessions{m: map[string]deviceSession{}} }

func (d *deviceSessions) put(id string, s deviceSession) {
	d.mu.Lock()
	d.m[id] = s
	// Opportunistic GC of expired sessions so the map can't grow unbounded.
	for k, v := range d.m {
		if time.Now().After(v.expiresAt) {
			delete(d.m, k)
		}
	}
	d.mu.Unlock()
}

func (d *deviceSessions) get(id string) (deviceSession, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.m[id]
	return s, ok
}

func (d *deviceSessions) del(id string) {
	d.mu.Lock()
	delete(d.m, id)
	d.mu.Unlock()
}

// deviceStartResult / devicePollResult are the daemon-internal shapes returned by
// the (injectable) GitHub calls.
type deviceStartResult struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       int
	Interval        int
}

// devicePollState mirrors the states a2's client polls on.
type devicePollState string

const (
	devicePending  devicePollState = "authorization_pending"
	deviceSlowDown devicePollState = "slow_down"
	deviceExpired  devicePollState = "expired"
	deviceDenied   devicePollState = "access_denied"
	deviceAuthd    devicePollState = "authorized"
)

type devicePollResult struct {
	State devicePollState
	Token string // set only on authorized
	Login string // gh login, set only on authorized
	ID    int64
}

// githubDeviceEnabled reports whether device flow is configured (a client id is
// set). When false the endpoints return 501 and the PAT path is the only route.
func (s *Server) githubDeviceEnabled() bool { return strings.TrimSpace(s.githubClientID) != "" }

// handleGitHubDeviceStart begins a device-flow authorization.
func (s *Server) handleGitHubDeviceStart(w http.ResponseWriter, r *http.Request) {
	if !s.githubDeviceEnabled() {
		writeError(w, http.StatusNotImplemented, errors.New("guided GitHub sign-in isn't configured on this daemon (no OAuth app); use `dejima auth push --github` with a token instead"))
		return
	}
	owner, _ := s.callerGHScope(r.Context())
	res, err := s.ghDeviceStartFn(r.Context(), s.githubClientID)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("start github device flow: %w", err))
		return
	}
	sid, err := randomID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	interval := res.Interval
	if interval <= 0 {
		interval = 5
	}
	s.deviceSessions.put(sid, deviceSession{
		deviceCode: res.DeviceCode,
		owner:      owner,
		expiresAt:  time.Now().Add(time.Duration(max(res.ExpiresIn, 60)) * time.Second),
		interval:   interval,
	})
	writeJSON(w, http.StatusOK, GitHubDeviceStartResponse{
		SessionID:       sid,
		UserCode:        res.UserCode,
		VerificationURI: res.VerificationURI,
		ExpiresIn:       res.ExpiresIn,
		Interval:        interval,
		Scopes:          deviceScopes,
	})
}

// handleGitHubDevicePoll checks a pending flow and, once authorized, stores the
// token as the polling caller's tenant identity.
func (s *Server) handleGitHubDevicePoll(w http.ResponseWriter, r *http.Request) {
	if !s.githubDeviceEnabled() {
		writeError(w, http.StatusNotImplemented, errors.New("guided GitHub sign-in isn't configured on this daemon"))
		return
	}
	var req GitHubDevicePollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if err := githubid.ValidateName(strings.TrimSpace(req.Name)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	owner, ownsAll := s.callerGHScope(r.Context())
	if !ownsAll && (req.Default || req.Shared) {
		writeError(w, http.StatusForbidden, errors.New("only the host owner can set a github identity as default or shared"))
		return
	}
	sess, ok := s.deviceSessions.get(req.SessionID)
	if !ok || time.Now().After(sess.expiresAt) {
		s.deviceSessions.del(req.SessionID)
		writeJSON(w, http.StatusOK, GitHubDevicePollResponse{State: string(deviceExpired)})
		return
	}
	// Tenant binding: only the tenant that STARTED the flow may poll it.
	if sess.owner != owner {
		writeError(w, http.StatusForbidden, errors.New("this device-flow session belongs to another tenant"))
		return
	}

	res, err := s.ghDevicePollFn(r.Context(), s.githubClientID, sess.deviceCode)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("poll github device flow: %w", err))
		return
	}
	switch res.State {
	case deviceAuthd:
		name := strings.TrimSpace(req.Name)
		if _, err := githubid.Update(func(st *githubid.Store) error {
			st.PutOwned(githubid.Identity{
				Name: name, Login: res.Login, ID: res.ID, Token: res.Token,
				Owner: owner, Shared: ownsAll && req.Shared,
			})
			if req.Default {
				_ = st.SetDefaultFor(owner, name)
			}
			return nil
		}); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.deviceSessions.del(req.SessionID)
		s.log.Info("github identity captured via device flow", "name", name, "login", res.Login, "owner", owner)
		writeJSON(w, http.StatusOK, GitHubDevicePollResponse{State: string(deviceAuthd), Identity: name, Login: res.Login})
	case deviceExpired, deviceDenied:
		s.deviceSessions.del(req.SessionID)
		writeJSON(w, http.StatusOK, GitHubDevicePollResponse{State: string(res.State)})
	default: // pending / slow_down — keep waiting, echo the interval so the client paces itself
		if res.State == deviceSlowDown {
			// GitHub's slow_down means "back off by +5s"; persist the bump so every
			// subsequent poll (and the echoed interval the client honors) reflects it.
			sess.interval += 5
			s.deviceSessions.put(req.SessionID, sess)
		}
		writeJSON(w, http.StatusOK, GitHubDevicePollResponse{State: string(res.State), Interval: sess.interval})
	}
}

// randomID returns a 128-bit opaque session id.
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// githubDeviceStart calls GitHub's device-code endpoint. The real implementation
// behind s.ghDeviceStartFn.
func githubDeviceStart(ctx context.Context, clientID string) (deviceStartResult, error) {
	form := url.Values{"client_id": {clientID}, "scope": {deviceScopes}}
	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
		ErrorDesc       string `json:"error_description"`
	}
	if err := githubPostForm(ctx, githubDeviceCodeURL, form, "", &out); err != nil {
		return deviceStartResult{}, err
	}
	if out.Error != "" {
		return deviceStartResult{}, fmt.Errorf("github: %s (%s)", out.Error, out.ErrorDesc)
	}
	return deviceStartResult{
		DeviceCode: out.DeviceCode, UserCode: out.UserCode, VerificationURI: out.VerificationURI,
		ExpiresIn: out.ExpiresIn, Interval: out.Interval,
	}, nil
}

// githubDevicePoll exchanges the device_code for a token (or reports the pending
// state), then resolves the gh login. The real implementation behind
// s.ghDevicePollFn.
func githubDevicePoll(ctx context.Context, clientID, deviceCode string) (devicePollResult, error) {
	form := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := githubPostForm(ctx, githubTokenURL, form, "", &out); err != nil {
		return devicePollResult{}, err
	}
	if out.Error != "" {
		switch out.Error {
		case "authorization_pending":
			return devicePollResult{State: devicePending}, nil
		case "slow_down":
			return devicePollResult{State: deviceSlowDown}, nil
		case "expired_token":
			return devicePollResult{State: deviceExpired}, nil
		case "access_denied":
			return devicePollResult{State: deviceDenied}, nil
		default:
			return devicePollResult{}, fmt.Errorf("github: %s", out.Error)
		}
	}
	if out.AccessToken == "" {
		return devicePollResult{State: devicePending}, nil
	}
	login, id, err := githubWhoAmI(ctx, out.AccessToken)
	if err != nil {
		return devicePollResult{}, err
	}
	return devicePollResult{State: deviceAuthd, Token: out.AccessToken, Login: login, ID: id}, nil
}

// githubWhoAmI resolves the login + numeric id for a token (GET /user).
func githubWhoAmI(ctx context.Context, token string) (string, int64, error) {
	var out struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	}
	if err := githubGet(ctx, githubUserURL, token, &out); err != nil {
		return "", 0, err
	}
	return out.Login, out.ID, nil
}

func githubPostForm(ctx context.Context, endpoint string, form url.Values, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return githubDo(req, out)
}

func githubGet(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return githubDo(req, out)
}

func githubDo(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("github returned %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
