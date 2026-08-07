package main

import "testing"

func TestExtractGatewayToken(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:18789/?token=abcd1234efgh":                "abcd1234efgh",
		"Gateway Token: abcd1234efgh":                               "abcd1234efgh",
		"OPENCLAW_GATEWAY_TOKEN=abcd1234efgh":                       "abcd1234efgh",
		`{"token":"abcd1234efgh"}`:                                  "abcd1234efgh",
		"ws://localhost:64046  token   abcd1234efgh   (paste this)": "abcd1234efgh",
		"no token here":                                             "",
		"the token is short7":                                       "",
	}
	for in, want := range cases {
		if got := extractGatewayToken(in); got != want {
			t.Errorf("extractGatewayToken(%q) = %q, want %q", in, got, want)
		}
	}
}

// parseTokenOutput reads the token from a DashboardTokenCmd's output — normally a
// bare token on its own line (openclaw config get), else a labeled form.
func TestParseTokenOutput(t *testing.T) {
	cases := map[string]string{
		"abcd1234efgh\n":                    "abcd1234efgh", // the common case: bare token
		"  abcd1234efgh  ":                  "abcd1234efgh", // trimmed
		"gateway.auth.token = abcd1234efgh": "abcd1234efgh", // labeled fallback
		"Warning: deprecated\nabcd1234efgh": "abcd1234efgh", // last line wins over noise
		"":                                  "",
		"short7":                            "", // below the min length, no token elsewhere
	}
	for in, want := range cases {
		if got := parseTokenOutput(in); got != want {
			t.Errorf("parseTokenOutput(%q) = %q, want %q", in, got, want)
		}
	}
}
