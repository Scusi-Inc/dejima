package secrets

import (
	"fmt"
	"regexp"
	"strings"
)

// Secret names become environment variables inside the island, which makes the
// name itself a security surface: several variables are interpreted by the
// dynamic loader, the shell, or a language runtime BEFORE any program logic
// runs, so setting one turns "add a secret" into code execution. Others would
// switch off Dejima's own containment.
//
// This file is the guard. It is deliberately a separate, heavily-commented unit
// because it is the part of the secrets manager that is painful to get wrong
// and easy to under-think.

// nameRe is the portable environment-variable name grammar. Anything outside it
// cannot be exported by a shell anyway, so rejecting early beats writing a file
// that silently breaks every new session.
var nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// deniedPrefixes are name prefixes rejected wholesale. Kept narrow on purpose:
// broad prefixes block legitimate names (NODE_ENV, GITHUB_TOKEN) and push
// operators toward workarounds, which is its own risk.
var deniedPrefixes = []string{
	"LD_",     // LD_PRELOAD / LD_LIBRARY_PATH / LD_AUDIT — loader injection
	"DYLD_",   // macOS equivalents (DYLD_INSERT_LIBRARIES)
	"GLIBC_",  // GLIBC_TUNABLES is parsed in the loader (CVE-2023-4911)
	"BASH_",   // BASH_ENV is SOURCED by non-interactive bash
	"DEJIMA_", // the daemon's own channel: DEJIMA_TOKEN, DEJIMA_HOST, …
}

// deniedNames are exact names rejected regardless of prefix.
//
// Grouped by why, because "why is PAGER on a deny-list" is a fair question and
// the answer (git and many CLIs exec it) is not obvious a year from now.
var deniedNames = []string{
	// Shell interpretation — sourced, evaluated, or word-splitting.
	"ENV", "IFS", "PS4", "SHELLOPTS", "CDPATH",

	// Language runtimes that load code from a path or flag before main().
	"PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME",
	"NODE_OPTIONS", // --require can preload a file (NODE_ENV stays allowed)
	"PERL5LIB", "PERL5OPT", "RUBYOPT", "RUBYLIB",
	"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS",

	// Command resolution and programs other tools exec on your behalf.
	"PATH", "EDITOR", "VISUAL", "PAGER", "TERMINFO", "TERMCAP", "TMPDIR",
	"HOSTALIASES", "LOCALDOMAIN", "RESOLV_HOST_CONF",

	// git execs these directly. Note GIT_ is NOT a blanket prefix: GITHUB_TOKEN
	// must stay legal, and it does not match this list.
	"GIT_SSH", "GIT_SSH_COMMAND", "GIT_EXTERNAL_DIFF", "GIT_PAGER", "GIT_EDITOR",
	"GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_PROXY_COMMAND", "GIT_DIR", "GIT_WORK_TREE",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",

	// Process identity — repointing HOME moves every tool's config lookup.
	"HOME", "USER", "LOGNAME", "SHELL",

	// Dejima containment. THIS is the row that makes the deny-list more than a
	// footgun guard: the daemon sets HTTPS_PROXY so island egress routes through
	// the observable proxy. A secret by that name silently disables egress
	// visibility and `dejima egress allow/deny` — the containment story switched
	// off by a config field.
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY",
}

// ValidateName reports whether name may be used as a secret. The error names
// the reason, since "rejected" without a why invites the operator to work
// around it.
//
// Matching is CASE-INSENSITIVE. Environment variables are case-sensitive on
// Unix, but `http_proxy` and `HTTP_PROXY` are both honoured by common tooling,
// so a case-sensitive check would leave the lowercase spelling wide open.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("secret name is empty")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("invalid secret name %q — must start with a letter or underscore "+
			"and contain only letters, digits, and underscores", name)
	}
	upper := strings.ToUpper(name)
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(upper, p) {
			return fmt.Errorf("%q is reserved (names starting with %s change how programs load "+
				"or reach the network, so setting one could run code or bypass Dejima's egress gate)", name, p)
		}
	}
	for _, d := range deniedNames {
		if upper == d {
			return fmt.Errorf("%q is reserved (it changes how programs are found, loaded, or "+
				"connected, so setting it could run code or bypass Dejima's egress gate)", name)
		}
	}
	return nil
}

// Reserved reports whether name is rejected, without building an error — for
// callers that only need the predicate (e.g. UI hints).
func Reserved(name string) bool { return ValidateName(name) != nil }
