package main

import (
	"regexp"
	"strings"
	"testing"
)

// TestHeaderSwitchKeyActuallySwitches: the key the header advertises next to
// the server name must be the key that changes servers.
//
// It said "[s] switch" for a release after `s` stopped switching — `s` became
// the highlighted row's settings menu — so an operator who noticed they were on
// the wrong server and pressed the key the header named got a menu for whatever
// island the cursor happened to be on. Nothing errored, which is why it lasted.
//
// The assertion is deliberately made against the RENDERED header rather than
// the constant: reading switchKey back out of the string is what ties the
// advertisement to the binding, and a header that stops naming a key fails the
// non-emptiness check below rather than passing vacuously.
func TestHeaderSwitchKeyActuallySwitches(t *testing.T) {
	m := seededModel(t, island("alpha"))
	header := m.renderHeader()

	if !strings.Contains(header, "switch") {
		t.Fatalf("the header no longer offers a way to switch servers; got:\n%s", header)
	}
	// Pull the key out of "[X] switch" as an operator reads it, ANSI and all.
	adv := regexp.MustCompile(`\[([^\]]{1,3})\]\x1b?\[?[0-9;]*m? ?switch`)
	plain := stripANSITest(header)
	match := adv.FindStringSubmatch(plain)
	if match == nil {
		t.Fatalf("could not find an advertised switch key in the header; got:\n%s", plain)
	}
	advertised := match[1]
	if advertised != switchKey {
		t.Errorf("header advertises %q but the binding is %q", advertised, switchKey)
	}

	// Press exactly what it says, from a plain island row — the situation the
	// old header got wrong.
	res, _ := m.handleKey(key(advertised))
	got := res.(tuiModel)
	if got.switcher == nil {
		t.Errorf("pressing the advertised key %q did not open the connection switcher", advertised)
	}
	if got.menu != nil {
		t.Errorf("pressing %q opened a row menu instead of the switcher", advertised)
	}

	// The regression, held down from the other side: `s` on a row is the row's
	// menu now, and must not be mistaken for a server control again.
	res, _ = m.handleKey(key("s"))
	sPressed := res.(tuiModel)
	if sPressed.switcher != nil {
		t.Error("`s` opened the connection switcher — it is the row menu now")
	}
	if sPressed.menu == nil {
		t.Error("`s` on an island row should open that row's menu")
	}
}

// stripANSITest removes SGR sequences so the test reads the header the way a
// person does. Local to this test: the TUI renders styled strings everywhere,
// and asserting on them raw is how a cosmetic restyle breaks an unrelated test.
func stripANSITest(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(s, "")
}
