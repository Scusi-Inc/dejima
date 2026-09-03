package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/secrets"
)

// secretsTestEnv points HOME at a temp dir and forces the file backend, so a
// test never writes into the operator's real login Keychain.
func secretsTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(secrets.BackendEnvVar, "file")
}

// callSecrets invokes a secrets handler directly. The handlers read the island
// from the path value, so the route is registered on a bare mux — this exercises
// the handler's own gate rather than the role table, which is asserted
// separately in the roleauth tests.
func callSecrets(t *testing.T, s *Server, method, path, body string, asIsland string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/islands/{name}/secrets", s.handleListSecrets)
	mux.HandleFunc("PUT /v1/islands/{name}/secrets/{key}", s.handlePutSecret)
	mux.HandleFunc("DELETE /v1/islands/{name}/secrets/{key}", s.handleDeleteSecret)

	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rdr)
	if asIsland != "" {
		req = req.WithContext(context.WithValue(req.Context(), tokenIslandKey{}, asIsland))
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// The one boundary in the secrets manager that is real and enforceable: an
// island token may READ names (they reveal nothing its own environment doesn't)
// but must never WRITE. An agent that can plant a value its peers trust is an
// escalation path, and unlike "hiding" values, this one is actually achievable.
func TestIslandTokenCannotWriteSecrets(t *testing.T) {
	secretsTestEnv(t)
	s := newAuditServer(t)

	rr := callSecrets(t, s, http.MethodPut, "/v1/islands/wildfire/secrets/EXPO_TOKEN",
		`{"value":"tok-abc"}`, "wildfire")
	if rr.Code != http.StatusForbidden {
		t.Errorf("PUT as island token = %d, want 403", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "may not set secrets") {
		t.Errorf("refusal should explain why; got %s", rr.Body.String())
	}

	rr = callSecrets(t, s, http.MethodDelete, "/v1/islands/wildfire/secrets/EXPO_TOKEN", "", "wildfire")
	if rr.Code != http.StatusForbidden {
		t.Errorf("DELETE as island token = %d, want 403", rr.Code)
	}

	// Reading names stays allowed.
	rr = callSecrets(t, s, http.MethodGet, "/v1/islands/wildfire/secrets", "", "wildfire")
	if rr.Code != http.StatusOK {
		t.Errorf("GET as island token = %d, want 200 — agents may list names", rr.Code)
	}
}

// Values go in and never come back out. If a response ever carried one, the
// whole "values never leave the daemon" claim would be false — so assert it on
// the wire, not just on the type.
func TestSecretValueNeverAppearsInAnyResponse(t *testing.T) {
	secretsTestEnv(t)
	s := newAuditServer(t)
	const value = "tok-super-secret-9876"

	rr := callSecrets(t, s, http.MethodPut, "/v1/islands/wildfire/secrets/EXPO_TOKEN",
		`{"value":"`+value+`"}`, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), value) {
		t.Fatalf("the PUT response echoed the value: %s", rr.Body.String())
	}

	rr = callSecrets(t, s, http.MethodGet, "/v1/islands/wildfire/secrets", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET = %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, value) {
		t.Fatalf("the LIST response contained the value: %s", body)
	}

	var resp SecretsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Secrets) != 1 || resp.Secrets[0].Name != "EXPO_TOKEN" {
		t.Fatalf("list = %+v, want one EXPO_TOKEN", resp.Secrets)
	}
	if resp.Secrets[0].Fingerprint != secrets.Fingerprint(value) {
		t.Errorf("fingerprint doesn't match the value's")
	}
}

// A reserved name must be refused at the API too — the CLI is not the only
// caller, and the SDK reaches this path directly.
func TestAPIRejectsReservedSecretNames(t *testing.T) {
	secretsTestEnv(t)
	s := newAuditServer(t)

	for _, name := range []string{"PATH", "LD_PRELOAD", "HTTPS_PROXY", "BASH_ENV"} {
		rr := callSecrets(t, s, http.MethodPut, "/v1/islands/wildfire/secrets/"+name, `{"value":"x"}`, "")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", name, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "reserved") {
			t.Errorf("%s: refusal should say it's reserved; got %s", name, rr.Body.String())
		}
	}
}
