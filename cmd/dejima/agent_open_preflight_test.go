package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Two failures present to the operator as the same dead console:
//
//	tunnel up, gateway absent    → gatewayReady catches this
//	gateway up, no provider key  → nothing could catch this; hence the preflight
//
// The second is not a probe problem. A keyless gateway answers gatewayReady's
// GET perfectly and then fails every task. The daemon already knows the answer
// from registry data before any connection exists, so it is asked before the
// tunnel is dialled rather than inferred from it.

func preflightOutput(t *testing.T, agentType, provider, authState string) string {
	t.Helper()
	var b strings.Builder
	providerKeyPreflight(&b, agentType, provider, authState)
	return b.String()
}

func TestPreflightWarnsWhenTheKeyIsMissing(t *testing.T) {
	out := preflightOutput(t, "openclaw", "anthropic", authStateMissingProviderKey)
	if out == "" {
		t.Fatal("a key-requiring agent with no key must be warned about")
	}
	for _, want := range []string{
		"openclaw",     // which agent
		"anthropic",    // which provider, so the fix is unambiguous
		"provider set", // the command that fixes it
	} {
		if !strings.Contains(out, want) {
			t.Errorf("warning should mention %q:\n%s", want, out)
		}
	}
}

// The state where no provider is set at all reads differently from "provider X
// has no key", and quoting an empty provider name would be worse than useless.
func TestPreflightHandlesNoProviderConfigured(t *testing.T) {
	out := preflightOutput(t, "openclaw", "", authStateMissingProviderKey)
	if !strings.Contains(out, "No provider is configured") {
		t.Errorf("an unset provider should say so rather than quoting an empty name:\n%s", out)
	}
	if strings.Contains(out, `""`) {
		t.Errorf("empty provider name should not be quoted into the message:\n%s", out)
	}
}

// A warning that fires on healthy agents is one nobody reads. The daemon leaves
// AuthState empty both for agents whose key resolves AND for frameworks the
// provider subsystem doesn't apply to (claude-code, codex) — neither should be
// warned.
func TestPreflightSaysNothingWhenTheAgentIsFine(t *testing.T) {
	for _, authState := range []string{"", "some-future-state"} {
		if out := preflightOutput(t, "openclaw", "anthropic", authState); out != "" {
			t.Errorf("authState %q must not warn:\n%s", authState, out)
		}
	}
}

// The two failures can be true at once — the report that prompted this work
// showed "Default model · Off" alongside a disconnect. Stacked failures are only
// untangleable if each message says something the other doesn't, so this asserts
// they do not collapse into the same advice.
//
// Deliberately not a golden-string test: it checks that the DISTINGUISHING
// content is present in one and absent from the other, which is the property
// that matters and survives rewording.
func TestPreflightAndGatewayMessagesAreDistinguishable(t *testing.T) {
	preflight := preflightOutput(t, "openclaw", "anthropic", authStateMissingProviderKey)

	// The gateway-absent path talks about waiting and about nothing listening.
	// The preflight must not, or an operator reading it will wait for something
	// that has already arrived.
	for _, mustNot := range []string{"nothing is serving", "Waiting for the gateway", "install"} {
		if strings.Contains(preflight, mustNot) {
			t.Errorf("the provider-key warning must not read as the gateway-absent one; found %q:\n%s",
				mustNot, preflight)
		}
	}
	// And it must carry the thing the gateway message cannot: the remedy is a
	// credential, not patience.
	if !strings.Contains(preflight, "provider set") {
		t.Errorf("the provider-key warning must name a credential remedy, not a wait:\n%s", preflight)
	}
}

// The preflight turns on one string comparison. If the daemon renames that
// state, this warning silently stops firing and the operator is back to a
// console that connects and does nothing — with nothing red anywhere to say so.
// A rename is a plausible edit: it is an unexported literal on the daemon side
// with no compile-time link to here.
//
// Asserting `authStateMissingProviderKey == "missing-provider-auth"` would just
// restate the constant and pass forever. So this reads the daemon's source and
// requires the literal to still be there, the same way the primary-launch parity
// test derives its expectation from agentLaunchScript rather than hardcoding it.
func TestAuthStateConstantMatchesTheDaemon(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "server.go"))
	if err != nil {
		t.Fatalf("read the daemon source: %v", err)
	}
	// agentProviderStatus is the one place that produces the value.
	const producer = "func agentProviderStatus("
	src := string(b)
	i := strings.Index(src, producer)
	if i < 0 {
		t.Fatal("agentProviderStatus is gone from internal/api/server.go — this guard is no longer " +
			"reading the function it guards, and the preflight's trigger is unverified")
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, `"`+authStateMissingProviderKey+`"`) {
		t.Errorf("the daemon no longer emits %q from agentProviderStatus, so `agent open`'s "+
			"provider-key preflight will never fire again — and nothing else would report that.\n"+
			"agentProviderStatus body:\n%s", authStateMissingProviderKey, body)
	}
}
