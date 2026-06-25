package main

import "testing"

// TestSessionTitle: "island/agent", or just "island" when no agent is named.
func TestSessionTitle(t *testing.T) {
	if got := sessionTitle("myrepo", "p2"); got != "myrepo/p2" {
		t.Errorf("sessionTitle(myrepo,p2) = %q, want myrepo/p2", got)
	}
	if got := sessionTitle("myrepo", ""); got != "myrepo" {
		t.Errorf("sessionTitle(myrepo,'') = %q, want myrepo", got)
	}
}

// TestSanitizeTitle: control bytes (incl. ESC/BEL, which would let a crafted
// name inject escape sequences) are stripped; ordinary text is untouched.
func TestSanitizeTitle(t *testing.T) {
	cases := map[string]string{
		"myrepo/p2":          "myrepo/p2",
		"my repo · shell":    "my repo · shell",
		"evil\x1b]0;pwned\a": "evil]0;pwned", // ESC + BEL stripped
		"a\nb\tc":            "abc",
		"\x7fdel":            "del",
	}
	for in, want := range cases {
		if got := sanitizeTitle(in); got != want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
