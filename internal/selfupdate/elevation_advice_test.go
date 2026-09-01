package selfupdate

import "testing"

// The advice a failed elevation prints is the ONLY thing the person sees, and
// for a client-only install it used to name a remedy that would have had them
// install a daemon service to update a client. These assert the properties that
// made that wrong, not the exact prose — rewording should be free, but losing
// the working remedy or re-promoting the server-only one should not be.
func TestElevationAdviceNamesTheRemedyThatWorksEverywhere(t *testing.T) {
	got := ElevationAdvice()

	// Must lead with the layout-independent fix. A client-only machine has no
	// NOPASSWD rule and no way to get one it should want.
	if !contains(got, "sudo dejima update") {
		t.Errorf("advice does not name `sudo dejima update`, the only remedy that\n"+
			"works for a client-only install:\n  %s", got)
	}

	// The service install may still be MENTIONED, but not as the fix — it is a
	// server-only optimisation. If it appears before the general remedy, the
	// reader does the wrong thing first.
	svc := index(got, "service install")
	if svc >= 0 && svc < index(got, "sudo dejima update") {
		t.Errorf("`service install` is offered BEFORE `sudo dejima update`, so a\n"+
			"client-only user is told to install a daemon service first:\n  %s", got)
	}
}

func contains(s, sub string) bool { return index(s, sub) >= 0 }

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
