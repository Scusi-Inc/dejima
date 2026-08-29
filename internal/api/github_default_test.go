package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// POST /v1/credentials/github/{name}/default
//
// The gap this closes cost an operator an hour: `github connect` without
// --default adds a SECOND identity, the resolver picks the DEFAULT rather than
// the newest, and there was no route to say which one to use. Identities could
// be added and not managed.

func seedIdentity(t *testing.T, h http.Handler, name, login string, dflt bool) {
	t.Helper()
	body := `{"login":"` + login + `","token":"t-` + name + `"`
	if dflt {
		body += `,"default":true`
	}
	body += `}`
	if rr := do(t, h, http.MethodPut, "/v1/credentials/github/"+name, body); rr.Code != http.StatusOK {
		t.Fatalf("seed %s: %d %s", name, rr.Code, rr.Body.String())
	}
}

func identitiesFrom(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	var out GitHubIdentitiesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := map[string]bool{}
	for _, m := range out.Identities {
		got[m.Name] = m.Default
	}
	return got
}

func TestSetGitHubDefaultRepointsAnExistingIdentity(t *testing.T) {
	h, _ := newTestServer(t)
	seedIdentity(t, h, "aoos", "aoos", true)
	seedIdentity(t, h, "github", "aoos", false)

	rr := do(t, h, http.MethodPost, "/v1/credentials/github/github/default", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("set default: %d %s", rr.Code, rr.Body.String())
	}
	got := identitiesFrom(t, rr.Body.Bytes())
	if !got["github"] {
		t.Error("the named identity should now be the default")
	}
	if got["aoos"] {
		t.Error("the previous default must be cleared — two defaults is not a state the resolver can use")
	}
}

// An unknown name is 404, never a new identity. Setting a default is a choice
// among credentials you already hold; creating one by side effect would leave a
// default pointing at an identity with no token behind it.
func TestSetGitHubDefaultRefusesAnUnknownIdentity(t *testing.T) {
	h, _ := newTestServer(t)
	seedIdentity(t, h, "aoos", "aoos", true)

	rr := do(t, h, http.MethodPost, "/v1/credentials/github/ghost/default", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	list := do(t, h, http.MethodGet, "/v1/credentials/github", "")
	got := identitiesFrom(t, list.Body.Bytes())
	if _, created := got["ghost"]; created {
		t.Error("a rejected set-default created an identity by side effect")
	}
	if !got["aoos"] {
		t.Error("the existing default must be untouched by a rejected call")
	}
}

// The route exists because PUT cannot do this: it requires a login and token.
// If that ever changes, this route is redundant — and if it does not, a caller
// forced through PUT would have to re-supply a credential it is not changing.
func TestPutStillRequiresACredentialSoDefaultNeedsItsOwnRoute(t *testing.T) {
	h, _ := newTestServer(t)
	rr := do(t, h, http.MethodPut, "/v1/credentials/github/aoos", `{"login":"aoos","default":true}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("PUT without a token = %d, want 400. If PUT now accepts a token-less "+
			"update, the separate default route is redundant and should be reconsidered "+
			"rather than left as a second way to do one thing", rr.Code)
	}
}
