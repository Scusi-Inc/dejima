package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The regression guard: `dejima uninstall` used to abort on `list islands: … (is
// the daemon running?)`, which made a BROKEN install the one install you could
// not uninstall — precisely when you most want to start clean. Reaching the
// daemon must never be a precondition for tearing down local state.
func TestUninstall_DaemonUnreachableIsNotFatal(t *testing.T) {
	body, err := os.ReadFile("uninstall.go")
	if err != nil {
		t.Fatalf("read uninstall.go: %v", err)
	}
	if regexp.MustCompile(`return fmt\.Errorf\("list islands`).Match(body) {
		t.Error("uninstall.go aborts when the daemon is unreachable — " +
			"the local teardown (service, binaries, ~/.dejima) needs no daemon and must still run")
	}
}

// With the daemon down we delete no island, so the operator has to be told what
// survived — and told in commands they can still run, since the binary that
// could enumerate islands has just been removed.
func TestDaemonDownNotice_NamesTheLeftovers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		purge bool
		want  []string
	}{
		{
			name:  "purge-all orphans surviving volumes",
			purge: true,
			// ~/.dejima is gone, so nothing tracks the volumes anymore. The
			// operator must hear "orphaned", not just "some volumes remain".
			want: []string{"docker volume ls", "no longer tracked", "won't re-adopt", "~/.dejima"},
		},
		{
			name:  "keep-islands still recovers the islands",
			purge: false,
			// Asserted as meaning, not vocabulary: this used to require the literal
			// word "re-adopts", which is Dejima's word for it and not one the
			// person reading this notice necessarily knows. What has to survive is
			// the promise — install again and your islands come back.
			want: []string{"docker volume ls", "installing Dejima again", "islands back"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := daemonDownNotice(tc.purge)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("notice is missing %q:\n%s", want, got)
				}
			}
			// Never claim islands were removed — nothing was touched.
			for _, forbidden := range []string{"All islands and data removed", "purged"} {
				if strings.Contains(got, forbidden) {
					t.Errorf("notice claims %q, but no island was touched:\n%s", forbidden, got)
				}
			}
		})
	}
}
