package wsl

import (
	"strings"
	"testing"
)

// Real stderr, from the field and from socat itself.
//
// The rule this replaces matched `contains "socat" && contains "No such file"`,
// which is satisfied by socat's OWN error when the daemon socket is missing. An
// operator was told socat wasn't installed, installed it (already newest
// version), ran again, and got the identical message — while the true cause,
// dejimad not running, was never named.
func TestClassifyStderr(t *testing.T) {
	for _, tc := range []struct {
		name       string
		raw        string
		wantSubstr string
		notSubstr  string
	}{
		{
			name:       "dash cannot find the binary — genuinely missing",
			raw:        "sh: 1: socat: not found",
			wantSubstr: "socat isn't installed",
		},
		{
			name:       "bash phrasing",
			raw:        "bash: socat: command not found",
			wantSubstr: "socat isn't installed",
		},
		{
			// THE FIELD CASE. socat ran; the daemon's socket did not exist.
			name: "socat runs and the daemon socket is absent",
			raw: `socat[123] E connect(5, AF=1 "/root/.dejima/dejimad.sock", 45): ` +
				`No such file or directory`,
			wantSubstr: "dejimad isn't running",
			notSubstr:  "socat isn't installed",
		},
		{
			name:       "a bare socket error with no socat prefix still blames the daemon",
			raw:        "connect: No such file or directory",
			wantSubstr: "dejimad isn't running",
			notSubstr:  "socat isn't installed",
		},
		{
			name:       "anything else is passed through rather than guessed at",
			raw:        "wsl.exe: the distro is stopped",
			wantSubstr: "the distro is stopped",
			notSubstr:  "socat isn't installed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyStderr(tc.raw)
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("classifyStderr(%q)\n  = %q\n want it to contain %q", tc.raw, got, tc.wantSubstr)
			}
			if tc.notSubstr != "" && strings.Contains(got, tc.notSubstr) {
				t.Errorf("classifyStderr(%q)\n  = %q\n must NOT claim %q", tc.raw, got, tc.notSubstr)
			}
		})
	}
}
