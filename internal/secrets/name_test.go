package secrets

import (
	"strings"
	"testing"
)

// A secret name becomes an environment variable inside the island, and several
// variables are interpreted by the loader, the shell, or a language runtime
// BEFORE any program logic runs. Letting one through turns "add a secret" into
// code execution in every new shell, so this table is the security core of the
// feature rather than input tidying.
func TestValidateNameRejectsDangerous(t *testing.T) {
	for _, tc := range []struct{ name, why string }{
		// Loader injection — runs attacker code in every dynamically linked
		// process, no exploit required.
		{"LD_PRELOAD", "loads a shared object ahead of libc"},
		{"LD_LIBRARY_PATH", "redirects the library search path"},
		{"LD_AUDIT", "loads an auditing library"},
		{"DYLD_INSERT_LIBRARIES", "macOS LD_PRELOAD"},
		{"GLIBC_TUNABLES", "parsed inside the loader (CVE-2023-4911)"},

		// Shell interpretation.
		{"BASH_ENV", "SOURCED by non-interactive bash — direct execution"},
		{"BASH_FUNC_x", "exported function definitions"},
		{"ENV", "sourced by sh"},
		{"IFS", "changes word splitting"},
		{"PS4", "evaluated under set -x"},
		{"SHELLOPTS", "forces shell options"},

		// Language runtimes that load code before main().
		{"PYTHONPATH", "module search path"},
		{"PYTHONSTARTUP", "executed at interpreter start"},
		{"NODE_OPTIONS", "--require can preload a file"},
		{"PERL5OPT", "injects perl switches"},
		{"RUBYOPT", "injects ruby switches"},
		{"JAVA_TOOL_OPTIONS", "injects JVM args"},

		// Command resolution, and programs other tools exec for you.
		{"PATH", "hijacks every command"},
		{"EDITOR", "git and others exec it"},
		{"PAGER", "git execs it"},
		{"GIT_SSH_COMMAND", "git execs it verbatim"},
		{"GIT_EXTERNAL_DIFF", "git execs it verbatim"},
		{"GIT_PROXY_COMMAND", "git execs it verbatim"},

		// Identity — repointing HOME moves every tool's config lookup.
		{"HOME", "moves config resolution"},
		{"SHELL", "changes the interpreter"},

		// Dejima containment. The most important row: the daemon sets
		// HTTPS_PROXY so island egress goes through the observable proxy, so a
		// secret by this name silently disables egress visibility and
		// `dejima egress allow/deny`.
		{"HTTPS_PROXY", "disables the egress gate"},
		{"HTTP_PROXY", "disables the egress gate"},
		{"NO_PROXY", "carves holes in the egress gate"},
		{"DEJIMA_TOKEN", "the island's own daemon credential"},
		{"DEJIMA_HOST", "repoints the daemon channel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateName(tc.name); err == nil {
				t.Errorf("%s was ACCEPTED but %s", tc.name, tc.why)
			}
		})
	}
}

// Environment variables are case-sensitive on Unix, but `http_proxy` and
// `HTTP_PROXY` are both honoured by common tooling — so a case-sensitive check
// would leave the lowercase spelling wide open.
func TestValidateNameIsCaseInsensitive(t *testing.T) {
	for _, name := range []string{
		"http_proxy", "https_proxy", "ld_preload", "Ld_Preload",
		"path", "Path", "bash_env", "dejima_token",
	} {
		if err := ValidateName(name); err == nil {
			t.Errorf("%q was accepted; the deny-list must not be case-sensitive", name)
		}
	}
}

// Over-blocking is its own risk: it pushes operators toward workarounds. These
// are the names people will actually reach for, and they must work.
func TestValidateNameAcceptsRealSecrets(t *testing.T) {
	for _, name := range []string{
		"EXPO_TOKEN",   // the case that started this
		"GITHUB_TOKEN", // must NOT be caught by the GIT_ family
		"GITHUB_API_KEY",
		"NPM_TOKEN",
		"NODE_ENV", // NODE_OPTIONS is denied; NODE_ENV is ordinary config
		"STRIPE_SECRET_KEY",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"SUPABASE_SERVICE_ROLE_KEY",
		"AWS_ACCESS_KEY_ID",
		"DATABASE_URL",
		"_INTERNAL",
		"A",
		"api_key_lowercase_is_fine",
	} {
		if err := ValidateName(name); err != nil {
			t.Errorf("%q was rejected but is a legitimate secret: %v", name, err)
		}
	}
}

// Names outside the portable grammar can't be exported by a shell anyway;
// rejecting early beats writing a file that silently breaks every new session.
func TestValidateNameRejectsMalformed(t *testing.T) {
	for _, name := range []string{
		"", "1STARTS_WITH_DIGIT", "has-a-dash", "has.a.dot", "has space",
		"has$dollar", "has=equals", "has\nnewline", "quote\"inside", "semi;colon",
	} {
		if err := ValidateName(name); err == nil {
			t.Errorf("%q was accepted but is not a valid environment variable name", name)
		}
	}
}

// A rejection an operator can't understand invites them to work around it, so
// the error has to name the reason rather than just refusing.
func TestValidateNameErrorsExplainWhy(t *testing.T) {
	err := ValidateName("LD_PRELOAD")
	if err == nil {
		t.Fatal("expected LD_PRELOAD to be rejected")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error should say the name is reserved; got %q", err)
	}
	if !strings.Contains(err.Error(), "run code") && !strings.Contains(err.Error(), "load") {
		t.Errorf("error should say why it's dangerous; got %q", err)
	}
}

func TestReservedMatchesValidate(t *testing.T) {
	for _, name := range []string{"PATH", "EXPO_TOKEN", "LD_PRELOAD", "GITHUB_TOKEN", "bad-name"} {
		if Reserved(name) != (ValidateName(name) != nil) {
			t.Errorf("Reserved(%q) disagrees with ValidateName", name)
		}
	}
}
