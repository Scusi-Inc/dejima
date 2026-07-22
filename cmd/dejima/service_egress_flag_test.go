package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestServiceInstallExposesDaemonFlags guards a gap that made the documented
// egress mitigation unusable: dejimad has --no-egress-proxy, but `dejima
// service install` had no way to bake it into the plist/unit. Because the
// proxy is default-ON and the service manager rewrites the daemon's args on
// every restart, a hand-run `dejimad --no-egress-proxy` does not survive — so
// without an install flag there is no supported way to persist the bypass at
// all.
//
// Any daemon flag an operator must be able to make permanent belongs here.
func TestServiceInstallExposesDaemonFlags(t *testing.T) {
	var install *cobra.Command
	for _, sub := range newServiceCmd().Commands() {
		if sub.Name() == "install" {
			install = sub
			break
		}
	}
	if install == nil {
		t.Fatal("`service install` subcommand not registered")
	}
	for _, name := range []string{"no-egress-proxy", "tcp", "token-tcp", "ssh", "audit"} {
		if install.Flags().Lookup(name) == nil {
			t.Errorf("`service install` is missing --%s; operators cannot persist it into the service", name)
		}
	}
}
