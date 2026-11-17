package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoos/dejima/internal/runtime"
)

// The daemon host's codex CONFIG must never cross into an island.
//
// /opt/host/codex used to be either/or: a pushed login, else the host's whole
// ~/.codex. The second branch carried config.toml along with the credentials,
// and config.toml describes the HOST — its localhost, its environment
// variables, its proxy. An operator who installed a local model behind a
// LiteLLM proxy on the daemon box got
//
//	model_provider = "litellm"
//	base_url       = "http://localhost:4000/v1"
//	env_key        = "LITELLM_API_KEY"
//
// copied into every island, where localhost is the container and the variable
// does not exist. Codex then refused to start with `Missing environment
// variable: LITELLM_API_KEY`, and nothing done inside the island fixed it: the
// shim re-copies the file on every agent start whenever the local one is
// absent, so a delete lasted until the next restart.
//
// These assert on the CONTENTS OF THE MOUNTED DIRECTORY rather than on which
// branch ran, because the bug is about what reaches the island. A refactor that
// goes back to mounting the host dir fails here even if it looks nothing like
// the original code.

// hostCodexDir stands up a daemon host that is logged into codex AND has
// configured a local proxy — the combination that broke.
func hostCodexDir(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"),
		[]byte(`{"tokens":{"access_token":"sk-host"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(
		"model_provider = \"litellm\"\n\n[model_providers.litellm]\n"+
			"base_url = \"http://localhost:4000/v1\"\nenv_key = \"LITELLM_API_KEY\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// codexMount returns the bind serving /opt/host/codex on the last container
// created, or fails.
func codexMount(t *testing.T, binds []runtime.BindMount) runtime.BindMount {
	t.Helper()
	for _, b := range binds {
		if b.ContainerPath == "/opt/host/codex" {
			return b
		}
	}
	t.Fatal("no /opt/host/codex mount at all. This guard must not pass by " +
		"denying islands the credential entirely — that trades one broken codex " +
		"for another")
	return runtime.BindMount{}
}

func TestCodexMountCarriesCredentialsNotTheHostConfig(t *testing.T) {
	h, f := newTestServer(t) // HOME is a temp dir
	hostCodexDir(t)

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}

	f.mu.Lock()
	binds := append([]runtime.BindMount(nil), f.lastCreate.BindMounts...)
	f.mu.Unlock()

	mount := codexMount(t, binds)
	entries, err := os.ReadDir(mount.HostPath)
	if err != nil {
		t.Fatalf("read the mounted dir %s: %v", mount.HostPath, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	// The credential still arrives — without this the test above is vacuous.
	var sawAuth bool
	for _, n := range names {
		if n == "auth.json" {
			sawAuth = true
		}
	}
	if !sawAuth {
		t.Errorf("the mount carries no auth.json, so no island can authenticate: %v", names)
	}

	// And the host's config does not.
	for _, n := range names {
		if n == "config.toml" {
			t.Errorf("the host's config.toml is on the island mount (%s). Every island "+
				"would inherit the daemon box's model_provider/base_url/env_key, which "+
				"describe the HOST and are wrong inside a container. Contents: %v",
				mount.HostPath, names)
		}
	}
}

// A host that is not logged into codex contributes NO mount, rather than an
// empty directory or the host dir. The negative control for the test above: it
// proves the assertion there is driven by the seeding, not by the mount being
// absent in every case.
func TestNoCodexLoginMeansNoCodexMount(t *testing.T) {
	h, f := newTestServer(t)
	// Deliberately no ~/.codex at all.

	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	f.mu.Lock()
	binds := append([]runtime.BindMount(nil), f.lastCreate.BindMounts...)
	f.mu.Unlock()

	for _, b := range binds {
		if b.ContainerPath == "/opt/host/codex" {
			t.Errorf("a host with no codex login still produced a codex mount from %s", b.HostPath)
		}
	}
}

// A pushed login keeps precedence over the daemon host's own, and still carries
// no config: the push is the more deliberate statement of which account islands
// should use.
func TestPushedCodexLoginWinsOverTheHostAndCarriesNoConfig(t *testing.T) {
	h, f := newTestServer(t)
	hostCodexDir(t)

	if rr := do(t, h, http.MethodPut, "/v1/credentials/codex",
		`{"credentials_json":"{\"tokens\":{\"access_token\":\"sk-pushed\"}}"}`); rr.Code != http.StatusNoContent {
		t.Fatalf("push: %d %s", rr.Code, rr.Body.String())
	}
	if rr := do(t, h, http.MethodPost, "/v1/islands", `{"repo":"r","name":"proj"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}

	f.mu.Lock()
	binds := append([]runtime.BindMount(nil), f.lastCreate.BindMounts...)
	f.mu.Unlock()

	mount := codexMount(t, binds)
	blob, err := os.ReadFile(filepath.Join(mount.HostPath, "auth.json"))
	if err != nil {
		t.Fatalf("no auth.json on the mount: %v", err)
	}
	if got := string(blob); got != `{"tokens":{"access_token":"sk-pushed"}}` {
		t.Errorf("the host's login overwrote the pushed one; got %s", got)
	}
	if _, err := os.Stat(filepath.Join(mount.HostPath, "config.toml")); err == nil {
		t.Error("config.toml reached the mount even on the pushed path")
	}
}
