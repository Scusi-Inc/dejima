package main

import (
	"strings"
	"testing"
)

// Both settings are required and NEITHER IS SUFFICIENT. Reporting OK on one
// would be worse than checking neither: it gives a reassuring answer to the
// exact question that went unanswered while a host sat dead for two weeks.
func TestUnattendedHostVerdict(t *testing.T) {
	for _, tc := range []struct {
		name       string
		docker     tristate
		login      tristate
		wantStatus string
		wantFix    bool
	}{
		{"both on: the only healthy combination", triYes, triYes, "OK", false},
		{"both off", triNo, triNo, "WARN", true},
		{"auto-login on, Docker won't start", triNo, triYes, "WARN", true},
		{"Docker set, but nobody ever signs in", triYes, triNo, "WARN", true},
		{"could not read either", triUnknown, triUnknown, "INFO", true},
		// A half-known state must not be rounded up to OK. This is the case that
		// would quietly reintroduce the original failure.
		{"docker on, login unknown", triYes, triUnknown, "INFO", true},
		{"login on, docker unknown", triUnknown, triYes, "INFO", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, detail, fix := unattendedHostVerdict(tc.docker, tc.login)
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q (detail: %s)", status, tc.wantStatus, detail)
			}
			if tc.wantFix && strings.TrimSpace(fix) == "" {
				t.Error("no remedy offered for a state that needs one")
			}
			if !tc.wantFix && strings.TrimSpace(fix) != "" {
				t.Errorf("offered a remedy for a healthy host: %q", fix)
			}
			if tc.wantFix && !strings.Contains(fix, "Automatic login") {
				t.Errorf("remedy omits automatic login, so following it leaves the host still broken:\n%s", fix)
			}
		})
	}
}

// The settings file changed name and key case between Docker versions, and an
// unreadable value must read as UNKNOWN rather than as off.
func TestAutoStartFromSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want tristate
	}{
		{"modern lowercase", `{"autoStart":true,"other":1}`, triYes},
		{"explicit false", `{"autoStart": false}`, triNo},
		{"capitalised key", `{"AutoStart": true}`, triYes},
		{"key absent entirely", `{"somethingElse": true}`, triUnknown},
		{"empty file", ``, triUnknown},
		// "autoStart" absent but the word true elsewhere must not be read as yes.
		{"true belongs to another key", `{"openUIOnStartup": true}`, triUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoStartFromSettings(tc.body); got != tc.want {
				t.Errorf("autoStartFromSettings(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
