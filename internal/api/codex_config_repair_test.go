package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The robust half of the codex-config fix: an island must not run a config that
// names an environment variable it does not have, WHATEVER route that config
// took to get there.
//
// The routes were each fixed separately and each fix missed the others: a host
// config copied in before any of this shipped still sits in the island's home
// volume (nothing overwrites an existing file), an operator may have written
// one by hand, a provider may since have been removed. So the check is on the
// property, not the provenance, and these tests drive init.sh's own function
// rather than a transcription of it.

// runConfigCheck evals config_selects_missing_env out of the real init.sh and
// asks it about cfg, with env applied. Returns true when the config is judged
// unusable.
func runConfigCheck(t *testing.T, cfg string, env ...string) bool {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	init := filepath.Join(root, "image", "agents", "codex", "init.sh")
	script := `
eval "$(sed -n '/^config_selects_missing_env()/,/^}/p' ` + init + `)"
if config_selects_missing_env ` + path + `; then echo UNUSABLE; else echo OK; fi
`
	c := exec.Command("bash", "-c", script)
	c.Env = append(os.Environ(), env...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("running the check failed: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))
	switch got {
	case "UNUSABLE":
		return true
	case "OK":
		return false
	}
	t.Fatalf("unexpected output %q", got)
	return false
}

// The reported bug, as the file actually looks on the operator's host.
const litellmConfig = `model = "gpt-5.6-luna"
model_provider = "litellm"
approval_policy = "never"

[model_providers.litellm]
name = "litellm"
base_url = "http://localhost:4000/v1"
env_key = "LITELLM_API_KEY"
`

func TestConfigNamingAnAbsentVariableIsRejected(t *testing.T) {
	if !runConfigCheck(t, litellmConfig, "LITELLM_API_KEY=") {
		t.Error("a config whose env_key is unset here cannot start codex " +
			"(\"Missing environment variable: `LITELLM_API_KEY`\") and must be replaced")
	}
}

// The same config is FINE once the island has the variable — the operator added
// the secret. Replacing it then would be destroying a working setup.
func TestConfigNamingAPresentVariableIsLeftAlone(t *testing.T) {
	if runConfigCheck(t, litellmConfig, "LITELLM_API_KEY=sk-present") {
		t.Error("the island has the variable, so this config works and is the " +
			"operator's to keep")
	}
}

// Dejima's own provider config names OPENAI_API_KEY, which the key file
// supplies. It must survive its own check.
func TestDejimaWrittenProviderConfigSurvives(t *testing.T) {
	cfg := `model_provider = "dejima"
[model_providers.dejima]
name = "local"
base_url = "http://host.docker.internal:11434/v1"
env_key = "OPENAI_API_KEY"
`
	if runConfigCheck(t, cfg, "OPENAI_API_KEY=local") {
		t.Error("Dejima writes this one itself; rejecting it would loop the repair")
	}
}

// The default config selects no provider at all (ChatGPT auth via auth.json).
// Nothing to check, nothing to replace.
func TestDefaultConfigWithNoProviderIsUsable(t *testing.T) {
	cfg := `sandbox_mode = "danger-full-access"
approval_policy = "never"
notify = ["/home/dejima/.codex/hooks/dejima-notify.sh"]
`
	if runConfigCheck(t, cfg) {
		t.Error("a config selecting no model_provider needs no key and must be kept")
	}
}

// Only the SELECTED provider's key matters. A config may define several; the
// ones not selected cannot stop codex starting, and treating them as fatal
// would move aside a working file.
func TestUnselectedProvidersDoNotCondemnTheConfig(t *testing.T) {
	cfg := `model_provider = "dejima"

[model_providers.dejima]
name = "local"
env_key = "OPENAI_API_KEY"

[model_providers.litellm]
name = "litellm"
env_key = "LITELLM_API_KEY"
`
	if runConfigCheck(t, cfg, "OPENAI_API_KEY=local", "LITELLM_API_KEY=") {
		t.Error("the selected provider's key is present; an unselected table " +
			"naming an absent variable is irrelevant to whether codex starts")
	}
}

// The inverse of the above, so the pair cannot both be satisfied by a check
// that simply ignores the tables it cannot parse.
func TestSelectedProviderIsTheOneChecked(t *testing.T) {
	cfg := `model_provider = "litellm"

[model_providers.dejima]
name = "local"
env_key = "OPENAI_API_KEY"

[model_providers.litellm]
name = "litellm"
env_key = "LITELLM_API_KEY"
`
	if !runConfigCheck(t, cfg, "OPENAI_API_KEY=local", "LITELLM_API_KEY=") {
		t.Error("the SELECTED provider's key is missing, so codex cannot start, " +
			"even though another table's key is present")
	}
}

// A config with no file at all is not a config to repair.
func TestMissingConfigIsNotRepaired(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	init := filepath.Join(root, "image", "agents", "codex", "init.sh")
	script := `
eval "$(sed -n '/^config_selects_missing_env()/,/^}/p' ` + init + `)"
if config_selects_missing_env /nonexistent/config.toml; then echo UNUSABLE; else echo OK; fi
`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "OK" {
		t.Errorf("an absent config is the normal first-start case, got %q", out)
	}
}
