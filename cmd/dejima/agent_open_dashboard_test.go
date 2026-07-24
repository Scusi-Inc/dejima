package main

import "testing"

func TestExtractGatewayToken(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:18789/?token=abcd1234efgh":                "abcd1234efgh",
		"Gateway Token: abcd1234efgh":                               "abcd1234efgh",
		"OPENCLAW_GATEWAY_TOKEN=abcd1234efgh":                       "abcd1234efgh",
		`{"token":"abcd1234efgh"}`:                                  "abcd1234efgh",
		"ws://localhost:64046  token   abcd1234efgh   (paste this)": "abcd1234efgh",
		"no token here":       "", // "here" is <8 chars, and nothing token-like
		"the token is short7": "", // 7 chars → rejected as not a real token
	}
	for in, want := range cases {
		if got := extractGatewayToken(in); got != want {
			t.Errorf("extractGatewayToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstURLIn(t *testing.T) {
	cases := map[string]string{
		"Dashboard: http://127.0.0.1:18789/dashboard?token=abc123": "http://127.0.0.1:18789/dashboard?token=abc123",
		"open https://localhost:18789/ui#/home in your browser.":   "https://localhost:18789/ui#/home",
		"no url here": "",
		"trailing paren (http://127.0.0.1:18789/x?t=1)": "http://127.0.0.1:18789/x?t=1",
	}
	for in, want := range cases {
		if got := firstURLIn(in); got != want {
			t.Errorf("firstURLIn(%q) = %q, want %q", in, got, want)
		}
	}
}

// The framework prints a URL for its own loopback address; we must rewrite the
// authority onto the tunnel while preserving the path AND the token query —
// otherwise the console still can't authenticate.
func TestLocalizeURL(t *testing.T) {
	got, err := localizeURL("http://127.0.0.1:18789/dashboard?token=SECRET", 62732)
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://localhost:62732/dashboard?token=SECRET" {
		t.Errorf("localizeURL = %q, want the tunnel host with path+token preserved", got)
	}

	// An https/wss dashboard URL is localized to plain http (the tunnel is http);
	// the fragment is preserved.
	got, _ = localizeURL("https://gw.internal:18789/ui#/agents", 5000)
	if got != "http://localhost:5000/ui#/agents" {
		t.Errorf("localizeURL https = %q, want http tunnel URL with fragment", got)
	}
}
