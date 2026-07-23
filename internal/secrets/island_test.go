package secrets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// islandStore returns a store rooted in a temp HOME with the OS keychain
// disabled — tests must never write into the operator's real login Keychain,
// where entries would survive the run and need manual cleanup.
func islandStore(t *testing.T) *IslandStore {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(BackendEnvVar, "file")
	s, err := OpenIsland()
	if err != nil {
		t.Fatalf("OpenIsland: %v", err)
	}
	return s
}

func TestSetGetRoundTrip(t *testing.T) {
	s := islandStore(t)

	m, err := s.Set("wildfire", "EXPO_TOKEN", "tok-abc-123", "aoos")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if m.Name != "EXPO_TOKEN" || m.SetBy != "aoos" {
		t.Errorf("meta = %+v", m)
	}
	if m.Fingerprint != Fingerprint("tok-abc-123") {
		t.Errorf("fingerprint = %q, want the value's", m.Fingerprint)
	}

	got, err := s.Get("wildfire", "EXPO_TOKEN")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "tok-abc-123" {
		t.Errorf("Get = %q, want the stored value", got)
	}
}

// Islands are separate blast radii — a secret set on one must be invisible to
// another, since that scoping is the main containment claim of the feature.
func TestSecretsAreIslandScoped(t *testing.T) {
	s := islandStore(t)

	if _, err := s.Set("wildfire", "EXPO_TOKEN", "wildfire-value", "aoos"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("the_old_ruins", "EXPO_TOKEN", "ruins-value", "aoos"); err != nil {
		t.Fatal(err)
	}

	for island, want := range map[string]string{
		"wildfire":      "wildfire-value",
		"the_old_ruins": "ruins-value",
	} {
		got, err := s.Get(island, "EXPO_TOKEN")
		if err != nil {
			t.Fatalf("Get(%s): %v", island, err)
		}
		if got != want {
			t.Errorf("%s: got %q, want %q — islands are leaking into each other", island, got, want)
		}
	}

	if _, err := s.Get("lincoln_analysis", "EXPO_TOKEN"); err == nil {
		t.Error("an island with no secrets returned one")
	}
}

// Rotation must preserve CreatedAt and move UpdatedAt. Rotation is the actual
// defence here, so whether it's happening has to be visible.
func TestRotationKeepsCreatedAt(t *testing.T) {
	s := islandStore(t)

	first, err := s.Set("wildfire", "EXPO_TOKEN", "old-value", "aoos")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Set("wildfire", "EXPO_TOKEN", "new-value", "aoos")
	if err != nil {
		t.Fatal(err)
	}

	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("CreatedAt changed on rotation: %v → %v", first.CreatedAt, second.CreatedAt)
	}
	if second.UpdatedAt.Before(first.UpdatedAt) {
		t.Errorf("UpdatedAt went backwards: %v → %v", first.UpdatedAt, second.UpdatedAt)
	}
	if second.Fingerprint == first.Fingerprint {
		t.Error("fingerprint didn't change after rotating to a different value")
	}

	got, _ := s.Get("wildfire", "EXPO_TOKEN")
	if got != "new-value" {
		t.Errorf("after rotation Get = %q, want new-value", got)
	}
}

// Meta is the projection that crosses the API. If a value can ever be
// serialized out of it, the whole "values never leave the daemon" claim fails —
// so assert it structurally, on the JSON.
func TestMetaNeverCarriesAValue(t *testing.T) {
	s := islandStore(t)
	const secret = "super-secret-value-9876"
	if _, err := s.Set("wildfire", "EXPO_TOKEN", secret, "aoos"); err != nil {
		t.Fatal(err)
	}

	metas, err := s.List("wildfire")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Fatalf("List returned %d, want 1", len(metas))
	}

	blob := mustJSON(t, metas)
	if strings.Contains(blob, secret) {
		t.Fatalf("the VALUE appears in serialized metadata: %s", blob)
	}
	if !strings.Contains(blob, "EXPO_TOKEN") {
		t.Errorf("metadata should carry the name: %s", blob)
	}
}

// The metadata file is bookkeeping, not the secret — but a stray value written
// there would be a plaintext leak on disk, so check the file itself.
func TestMetaFileHoldsNoValues(t *testing.T) {
	s := islandStore(t)
	const secret = "plaintext-should-not-be-here"
	if _, err := s.Set("wildfire", "EXPO_TOKEN", secret, "aoos"); err != nil {
		t.Fatal(err)
	}

	p, err := metaPath("wildfire")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if strings.Contains(string(b), secret) {
		t.Errorf("meta.json contains the raw value:\n%s", b)
	}

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("meta.json mode = %o, want 0600", perm)
	}
}

func TestRemoveAndPurge(t *testing.T) {
	s := islandStore(t)
	for _, n := range []string{"EXPO_TOKEN", "NPM_TOKEN"} {
		if _, err := s.Set("wildfire", n, "v-"+n, "aoos"); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.Remove("wildfire", "EXPO_TOKEN"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := s.Get("wildfire", "EXPO_TOKEN"); err == nil {
		t.Error("removed secret is still readable")
	}
	names, _ := s.Names("wildfire")
	if len(names) != 1 || names[0] != "NPM_TOKEN" {
		t.Errorf("after Remove names = %v, want [NPM_TOKEN]", names)
	}

	// Removing something absent is fine — teardown shouldn't have to check.
	if err := s.Remove("wildfire", "NOT_THERE"); err != nil {
		t.Errorf("removing an absent secret errored: %v", err)
	}

	// Purge: secrets must not outlive the island they were scoped to.
	if err := s.Purge("wildfire"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if names, _ := s.Names("wildfire"); len(names) != 0 {
		t.Errorf("after Purge names = %v, want none", names)
	}
	if _, err := s.Get("wildfire", "NPM_TOKEN"); err == nil {
		t.Error("a value survived Purge — it would outlive the island")
	}
	dir, _ := paths_IslandSecretsPath(t, "wildfire")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("island secrets dir survived Purge: %v", err)
	}
}

func TestListIsSortedAndValuesMatch(t *testing.T) {
	s := islandStore(t)
	for _, n := range []string{"ZULU_TOKEN", "ALPHA_TOKEN", "MIKE_TOKEN"} {
		if _, err := s.Set("isl", n, "value-of-"+n, "aoos"); err != nil {
			t.Fatal(err)
		}
	}
	metas, err := s.List("isl")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{metas[0].Name, metas[1].Name, metas[2].Name}
	want := []string{"ALPHA_TOKEN", "MIKE_TOKEN", "ZULU_TOKEN"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List order = %v, want %v", got, want)
		}
	}

	vals, err := s.Values("isl")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range want {
		if vals[n] != "value-of-"+n {
			t.Errorf("Values[%s] = %q", n, vals[n])
		}
	}
}

// A reserved name must be refused by the store, not just by the CLI — the API
// and SDK reach this path directly.
func TestSetRejectsReservedNames(t *testing.T) {
	s := islandStore(t)
	for _, n := range []string{"PATH", "LD_PRELOAD", "HTTPS_PROXY", "DEJIMA_TOKEN", "bad-name"} {
		if _, err := s.Set("wildfire", n, "value", "aoos"); err == nil {
			t.Errorf("%q was stored; reserved names must be refused at the store", n)
		}
	}
	// And nothing was written as a side effect.
	if names, _ := s.Names("wildfire"); len(names) != 0 {
		t.Errorf("rejected names left metadata behind: %v", names)
	}
}

func TestSetRejectsEmptyValue(t *testing.T) {
	s := islandStore(t)
	if _, err := s.Set("wildfire", "EXPO_TOKEN", "", "aoos"); err == nil {
		t.Error("an empty value was accepted")
	}
}

// Redaction targets the likeliest real leak: a tool echoing its configuration.
func TestRedact(t *testing.T) {
	s := islandStore(t)
	if _, err := s.Set("wildfire", "EXPO_TOKEN", "tok-abcdefgh-1234", "aoos"); err != nil {
		t.Fatal(err)
	}

	out := s.Redact("wildfire", "running: eas build --token tok-abcdefgh-1234 --platform ios")
	if strings.Contains(out, "tok-abcdefgh-1234") {
		t.Errorf("value survived redaction: %s", out)
	}
	if !strings.Contains(out, "[redacted:EXPO_TOKEN]") {
		t.Errorf("redaction should name the secret: %s", out)
	}
	if !strings.Contains(out, "--platform ios") {
		t.Errorf("redaction ate surrounding text: %s", out)
	}
}

// Masking very short values would redact ordinary words all over the output and
// make logs useless, while protecting something that was never really a secret.
func TestRedactSkipsShortValues(t *testing.T) {
	s := islandStore(t)
	if _, err := s.Set("wildfire", "SHORT_ONE", "abc", "aoos"); err != nil {
		t.Fatal(err)
	}
	const line = "abc is a common substring: abcdef, abcxyz"
	if got := s.Redact("wildfire", line); got != line {
		t.Errorf("a 3-char value was redacted, mangling ordinary text: %s", got)
	}
}

// A value containing another must be masked whole, not left with a
// partially-redacted tail that still leaks the rest.
func TestRedactLongestFirst(t *testing.T) {
	s := islandStore(t)
	if _, err := s.Set("isl", "SHORT_TOKEN", "abcdefgh", "aoos"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set("isl", "LONG_TOKEN", "abcdefgh-plus-more-tail", "aoos"); err != nil {
		t.Fatal(err)
	}
	out := s.Redact("isl", "value=abcdefgh-plus-more-tail")
	if strings.Contains(out, "plus-more-tail") {
		t.Errorf("longer value only partially redacted, leaking its tail: %s", out)
	}
}

// --- helpers ---------------------------------------------------------------

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func paths_IslandSecretsPath(t *testing.T, island string) (string, error) {
	t.Helper()
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dejima", "secrets", "islands", island), nil
}
