package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/aoos/dejima/internal/project"
)

func TestSanitizeTags(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{"nil", nil, nil},
		{"trims", map[string]string{" team ": " web "}, map[string]string{"team": "web"}},
		{"drops empty key", map[string]string{"": "x", "env": "prod"}, map[string]string{"env": "prod"}},
		{"all empty keys → nil", map[string]string{"  ": "x"}, nil},
		{"empty value kept", map[string]string{"flag": ""}, map[string]string{"flag": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeTags(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sanitizeTags(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestCreateStampsOwner: an island created over the trusted local surface (the
// host owner) is stamped to the host owner tenant, and the overview reports the
// caller's own owner/role for the client's own-vs-all lens.
func TestCreateStampsOwner(t *testing.T) {
	h, _ := newTestServer(t)

	rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"alpha","agent":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var info IslandInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.Owner != project.HostOwner() {
		t.Errorf("created island owner = %q, want host owner %q", info.Owner, project.HostOwner())
	}

	// Overview reports the caller's own identity (trusted local = host owner).
	rr = do(t, h, http.MethodGet, "/v1/overview", "")
	var ov OverviewResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ov)
	if ov.Owner != project.HostOwner() {
		t.Errorf("overview owner = %q, want %q", ov.Owner, project.HostOwner())
	}
	if ov.Role != "owner" {
		t.Errorf("overview role = %q, want owner", ov.Role)
	}
}
