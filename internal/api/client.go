package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/paths"
)

// Client is a thin HTTP client for the Dejima API.
type Client struct {
	httpc *http.Client
	base  string
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

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
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
	resp, err := c.httpc.Do(req)
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

// Health returns nil if dejimad is reachable and healthy.
func (c *Client) Health(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/v1/healthz", nil, nil)
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

// CreateIsland provisions a new island.
func (c *Client) CreateIsland(ctx context.Context, req CreateIslandRequest) (*IslandInfo, error) {
	var out IslandInfo
	if err := c.do(ctx, http.MethodPost, "/v1/islands", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteIsland tears down an island (purge).
func (c *Client) DeleteIsland(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/islands/"+name, nil, nil)
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

// ResetIsland clears agent state, preserves workspace.
func (c *Client) ResetIsland(ctx context.Context, name string) (*IslandInfo, error) {
	var out IslandInfo
	if err := c.do(ctx, http.MethodPost, "/v1/islands/"+name+"/reset", nil, &out); err != nil {
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
// stream open until ctx is canceled.
func (c *Client) StreamLogs(ctx context.Context, name string, follow bool) (io.ReadCloser, error) {
	q := ""
	if follow {
		q = "?follow=true"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/islands/"+name+"/logs"+q, nil)
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

func decodeErr(resp *http.Response) error {
	var er ErrorResponse
	if json.NewDecoder(resp.Body).Decode(&er) == nil && er.Error != "" {
		return fmt.Errorf("%s", er.Error)
	}
	return fmt.Errorf("http %d", resp.StatusCode)
}

// SubscribeWebhook registers a webhook URL with the daemon.
func (c *Client) SubscribeWebhook(ctx context.Context, url, secret string) (*events.Subscription, error) {
	req := SubscribeWebhookRequest{URL: url, Secret: secret}
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
	var out struct{ Revoked int `json:"revoked"` }
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

// IslandEvents returns the recent event log for one island.
func (c *Client) IslandEvents(ctx context.Context, name string) ([]events.Event, error) {
	var out []events.Event
	if err := c.do(ctx, http.MethodGet, "/v1/islands/"+name+"/events", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// DialSession opens a websocket against the daemon's session endpoint and
// returns the underlying connection.
func (c *Client) DialSession(ctx context.Context, name, label string) (*websocket.Conn, error) {
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
	wsURL := wsBase + "/v1/islands/" + name + "/session"
	if encoded := q.Encode(); encoded != "" {
		wsURL += "?" + encoded
	}
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient: c.httpc,
	})
	if err != nil {
		return nil, fmt.Errorf("dial session: %w", err)
	}
	return conn, nil
}
