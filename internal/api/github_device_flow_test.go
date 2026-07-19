package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aoos/dejima/internal/githubid"
)

// TestGitHubDeviceFlow drives start → poll(pending) → poll(authorized) with the
// GitHub calls stubbed, and asserts the captured token is stored as a host
// identity (the trusted-local caller is the host owner).
func TestGitHubDeviceFlow(t *testing.T) {
	srv, h, _ := wakeServer(t)
	srv.githubClientID = "test-client"
	srv.ghDeviceStartFn = func(context.Context, string) (deviceStartResult, error) {
		return deviceStartResult{DeviceCode: "dc", UserCode: "WXYZ-1234", VerificationURI: "https://github.com/login/device", ExpiresIn: 900, Interval: 5}, nil
	}
	authorized := false
	srv.ghDevicePollFn = func(context.Context, string, string) (devicePollResult, error) {
		if !authorized {
			return devicePollResult{State: devicePending}, nil
		}
		return devicePollResult{State: deviceAuthd, Token: "ghp_captured", Login: "amanda", ID: 42}, nil
	}

	// start
	rr := do(t, h, http.MethodPost, "/v1/credentials/github/device-flow/start", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("start = %d", rr.Code)
	}
	var start GitHubDeviceStartResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &start)
	if start.SessionID == "" || start.UserCode != "WXYZ-1234" || start.Scopes == "" {
		t.Fatalf("start response = %+v", start)
	}

	// poll — pending
	poll := func() GitHubDevicePollResponse {
		body := `{"session_id":"` + start.SessionID + `","name":"work"}`
		rr := do(t, h, http.MethodPost, "/v1/credentials/github/device-flow/poll", body)
		if rr.Code != http.StatusOK {
			t.Fatalf("poll = %d (%s)", rr.Code, rr.Body.String())
		}
		var p GitHubDevicePollResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &p)
		return p
	}
	if p := poll(); p.State != string(devicePending) {
		t.Fatalf("first poll state = %q, want pending", p.State)
	}

	// poll — authorized → identity stored
	authorized = true
	p := poll()
	if p.State != string(deviceAuthd) || p.Identity != "work" || p.Login != "amanda" {
		t.Fatalf("authorized poll = %+v", p)
	}
	store, _ := githubid.Load()
	id, ok := store.Resolve("work") // host tenant
	if !ok || id.Token != "ghp_captured" || id.Login != "amanda" {
		t.Fatalf("captured identity not stored: %+v ok=%v", id, ok)
	}
}

// TestGitHubDeviceFlowDisabled: with no client id, both endpoints are 501 and the
// PAT path is the only route.
func TestGitHubDeviceFlowDisabled(t *testing.T) {
	_, h, _ := wakeServer(t) // githubClientID defaults empty under a temp HOME/env
	rr := do(t, h, http.MethodPost, "/v1/credentials/github/device-flow/start", "")
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("start with device flow off = %d, want 501", rr.Code)
	}
	rr = do(t, h, http.MethodPost, "/v1/credentials/github/device-flow/poll", `{"session_id":"x","name":"work"}`)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("poll with device flow off = %d, want 501", rr.Code)
	}
}
