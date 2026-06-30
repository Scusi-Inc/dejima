package invite

import (
	"encoding/base64"
	"strings"
	"testing"
)

// b64 raw-url-encodes a literal JSON string for the negative test vectors.
func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestRoundTrip(t *testing.T) {
	cases := []Payload{
		{Host: "minion.ts.net:7274", Token: "sek_abc123", Role: "operator", Islands: []string{"webapp"}, Name: "minion", Label: "Amanda"},
		{Host: "10.0.0.5:7274", Token: "sek_min", Role: "viewer"}, // minimal
	}
	for _, want := range cases {
		blob, err := Encode(want)
		if err != nil {
			t.Fatalf("Encode(%+v): %v", want, err)
		}
		if !strings.HasPrefix(blob, Scheme) {
			t.Errorf("blob missing scheme prefix: %q", blob)
		}
		got, err := Decode(blob)
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		want.V = Version
		if got.Host != want.Host || got.Token != want.Token || got.Role != want.Role ||
			got.Name != want.Name || got.Label != want.Label || strings.Join(got.Islands, ",") != strings.Join(want.Islands, ",") {
			t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
		}
	}
}

func TestDecodeRejects(t *testing.T) {
	good, err := Encode(Payload{Host: "h:1", Token: "t", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	b64 := strings.TrimPrefix(good, Scheme)
	cases := []struct {
		name, in string
	}{
		{"no prefix", b64},
		{"empty", ""},
		{"bad base64", Scheme + "not*base64*"},
		{"bad json", Scheme + "YWJj"}, // base64 of "abc", not JSON
		{"wrong version", mustEncodeRaw(t, `{"v":2,"host":"h:1","token":"t","role":"owner"}`)},
		{"missing host", mustEncodeRaw(t, `{"v":1,"token":"t","role":"owner"}`)},
		{"missing token", mustEncodeRaw(t, `{"v":1,"host":"h:1","role":"owner"}`)},
		{"bad role", mustEncodeRaw(t, `{"v":1,"host":"h:1","token":"t","role":"god"}`)},
	}
	for _, c := range cases {
		if _, err := Decode(c.in); err == nil {
			t.Errorf("%s: Decode succeeded, want error", c.name)
		}
	}
}

// TestEncodeValidates: Encode refuses to mint a blob missing required fields,
// so a leaked-but-useless invite can never be produced.
func TestEncodeValidates(t *testing.T) {
	for _, p := range []Payload{
		{Token: "t", Role: "owner"},          // no host
		{Host: "h:1", Role: "owner"},         // no token
		{Host: "h:1", Token: "t"},            // no/blank role
		{Host: "h:1", Token: "t", Role: "x"}, // bad role
	} {
		if _, err := Encode(p); err == nil {
			t.Errorf("Encode(%+v) succeeded, want validation error", p)
		}
	}
}

// GoldenBlob is the frozen wire vector both the issue side (a1) and the join
// side (a2) assert against, so a refactor on either side that changes the bytes
// fails loudly. If this ever needs to change, it's a format-version bump.
const GoldenBlob = "dejima-invite:eyJ2IjoxLCJob3N0IjoibWluaW9uLnRzLm5ldDo3Mjc0IiwidG9rZW4iOiJzZWtfYWJjMTIzIiwicm9sZSI6Im9wZXJhdG9yIiwiaXNsYW5kcyI6WyJ3ZWJhcHAiXSwibmFtZSI6Im1pbmlvbiIsImxhYmVsIjoiQW1hbmRhIn0"

func TestGoldenBlob(t *testing.T) {
	want := Payload{V: 1, Host: "minion.ts.net:7274", Token: "sek_abc123", Role: "operator", Islands: []string{"webapp"}, Name: "minion", Label: "Amanda"}
	got, err := Encode(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != GoldenBlob {
		t.Errorf("golden blob drift:\n got  %s\n want %s", got, GoldenBlob)
	}
	back, err := Decode(GoldenBlob)
	if err != nil {
		t.Fatalf("Decode(golden): %v", err)
	}
	if back.Host != want.Host || back.Token != want.Token || back.Role != want.Role {
		t.Errorf("golden decode mismatch: %+v", back)
	}
}

func mustEncodeRaw(t *testing.T, jsonStr string) string {
	t.Helper()
	return Scheme + b64(jsonStr)
}
