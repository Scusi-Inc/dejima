package providercreds

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestPutResolveDefaultRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.dejima

	if _, err := Update(func(s *Store) error {
		s.Put(Provider{Name: "anthropic", APIKey: "sk-ant-abcd1234"})
		s.Put(Provider{Name: "openai", APIKey: "sk-proj-wxyz9876"})
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Default != "anthropic" {
		t.Errorf("first provider should be default; got %q", s.Default)
	}
	p, ok := s.Resolve("openai")
	if !ok || p.APIKey != "sk-proj-wxyz9876" {
		t.Errorf("Resolve(openai) = %+v ok=%v", p, ok)
	}
	if d, ok := s.Resolve(""); !ok || d.Name != "anthropic" {
		t.Errorf("Resolve(\"\") should yield the default; got %+v ok=%v", d, ok)
	}
	if _, ok := s.Resolve("nope"); ok {
		t.Error("Resolve(unknown) should be !ok")
	}
}

func TestListNeverLeaksKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := Update(func(s *Store) error {
		s.Put(Provider{Name: "anthropic", APIKey: "sk-ant-secrettail1234"})
		s.Put(Provider{Name: "shortish", APIKey: "abc123"})
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	s, _ := Load()
	for _, m := range s.List() {
		if strings.Contains(m.Hint, "secret") {
			t.Errorf("hint leaked key material: %q", m.Hint)
		}
		if !m.KeySet {
			t.Errorf("provider %q should report KeySet", m.Name)
		}
	}
	byName := map[string]Meta{}
	for _, m := range s.List() {
		byName[m.Name] = m
	}
	if got := byName["anthropic"].Hint; got != "…1234" {
		t.Errorf("anthropic hint = %q, want …1234", got)
	}
	if got := byName["shortish"].Hint; got != "set" { // <=8 chars → no tail shown
		t.Errorf("short-key hint = %q, want set", got)
	}
}

func TestEnvVarName(t *testing.T) {
	cases := []struct{ name, envVar, want string }{
		{"anthropic", "", "ANTHROPIC_API_KEY"},
		{"openai", "", "OPENAI_API_KEY"},
		{"my-openrouter", "", "MY_OPENROUTER_API_KEY"},
		{"azure", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_API_KEY"}, // explicit override wins
	}
	for _, c := range cases {
		if got := EnvVarName(Provider{Name: c.name, EnvVar: c.envVar}); got != c.want {
			t.Errorf("EnvVarName(%q,%q) = %q, want %q", c.name, c.envVar, got, c.want)
		}
	}
}

func TestDotEnv(t *testing.T) {
	got := DotEnv(Provider{Name: "anthropic", APIKey: "sk-ant-x"})
	if got != "ANTHROPIC_API_KEY=sk-ant-x\n" {
		t.Errorf("DotEnv basic = %q", got)
	}
	got = DotEnv(Provider{Name: "openai", APIKey: "k", BaseURL: "https://proxy.example/v1"})
	if !strings.Contains(got, "OPENAI_API_KEY=k\n") || !strings.Contains(got, "OPENAI_BASE_URL=https://proxy.example/v1\n") {
		t.Errorf("DotEnv with base_url = %q", got)
	}
}

func TestRemoveRedefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := Update(func(s *Store) error {
		s.Put(Provider{Name: "anthropic", APIKey: "a"})
		s.Put(Provider{Name: "openai", APIKey: "b"})
		return nil
	})
	if !s.Remove("anthropic") {
		t.Fatal("Remove(anthropic) returned false")
	}
	if s.Default != "openai" {
		t.Errorf("default should fall to openai; got %q", s.Default)
	}
	if s.Remove("nope") {
		t.Error("Remove(unknown) should return false")
	}
}

// TestUpdateIsSerializedAndAtomic mirrors githubid's concurrency guard: many
// concurrent Updates, none lost.
func TestUpdateIsSerializedAndAtomic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Update(func(s *Store) error {
				s.Put(Provider{Name: fmt.Sprintf("p%02d", i), APIKey: "k"})
				return nil
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Update: %v", err)
		}
	}
	s, _ := Load()
	if len(s.Providers) != n {
		t.Errorf("got %d providers, want %d (lost update)", len(s.Providers), n)
	}
}
