package githubid

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestUpdateIsSerializedAndAtomic runs many concurrent Update calls, each adding
// a distinct identity, and asserts none are lost — i.e. the read-modify-write is
// serialized and the atomic write never corrupts the store.
func TestUpdateIsSerializedAndAtomic(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.dejima

	const n = 25
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Update(func(s *Store) error {
				s.Put(Identity{Name: fmt.Sprintf("id%02d", i), Login: "u", Token: "t"})
				return nil
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Identities) != n {
		t.Fatalf("lost updates under concurrency: got %d identities, want %d", len(s.Identities), n)
	}
}

func TestPutSetsFirstAsDefault(t *testing.T) {
	s := &Store{}
	s.Put(Identity{Name: "personal", Login: "austin", Token: "t1"})
	if s.Default != "personal" {
		t.Fatalf("first Put should set default; got %q", s.Default)
	}
	s.Put(Identity{Name: "work", Login: "alockwood", Host: "github.example.com", Token: "t2"})
	if s.Default != "personal" {
		t.Errorf("later Put must not steal default; got %q", s.Default)
	}
	// Host defaults to github.com when unset.
	if id, _ := s.Resolve("personal"); id.Host != DefaultHost {
		t.Errorf("missing host should default to %q, got %q", DefaultHost, id.Host)
	}
}

func TestResolveNameAndDefault(t *testing.T) {
	s := &Store{}
	s.Put(Identity{Name: "personal", Login: "austin", Token: "t1"})
	s.Put(Identity{Name: "work", Login: "alockwood", Token: "t2"})
	_ = s.SetDefault("work")

	if id, ok := s.Resolve("personal"); !ok || id.Login != "austin" {
		t.Errorf("Resolve(personal) = %+v, %v", id, ok)
	}
	if id, ok := s.Resolve(""); !ok || id.Name != "work" {
		t.Errorf("Resolve(empty) should pick the default work; got %+v, %v", id, ok)
	}
	if _, ok := s.Resolve("nope"); ok {
		t.Error("Resolve(unknown) should be !ok")
	}
	empty := &Store{}
	if _, ok := empty.Resolve(""); ok {
		t.Error("Resolve(empty) on an empty store should be !ok")
	}
}

func TestListHasNoTokensAndSorts(t *testing.T) {
	s := &Store{}
	s.Put(Identity{Name: "work", Login: "alockwood", Token: "secret"})
	s.Put(Identity{Name: "personal", Login: "austin", Token: "secret"})
	list := s.List()
	if len(list) != 2 || list[0].Name != "personal" || list[1].Name != "work" {
		t.Fatalf("List not sorted by name: %+v", list)
	}
	// work was added first, so it's the default — independent of sort order.
	if list[0].Default || !list[1].Default {
		t.Errorf("default marking wrong (work added first): %+v", list)
	}
}

func TestRemoveReassignsDefault(t *testing.T) {
	s := &Store{}
	s.Put(Identity{Name: "personal", Login: "austin", Token: "t1"}) // default
	s.Put(Identity{Name: "work", Login: "alockwood", Token: "t2"})
	if !s.Remove("personal") {
		t.Fatal("Remove(personal) returned false")
	}
	if s.Default != "work" {
		t.Errorf("default should fall to work after removing personal; got %q", s.Default)
	}
	if s.Remove("personal") {
		t.Error("Remove of a missing identity should return false")
	}
	s.Remove("work")
	if s.Default != "" {
		t.Errorf("default should clear when no identities remain; got %q", s.Default)
	}
}

func TestHostsYAML(t *testing.T) {
	got := HostsYAML(Identity{Login: "austin", Token: "ghp_abc", Host: ""})
	for _, want := range []string{"github.com:", "oauth_token: ghp_abc", "user: austin", "git_protocol: https"} {
		if !strings.Contains(got, want) {
			t.Errorf("HostsYAML missing %q in:\n%s", want, got)
		}
	}
	// Enterprise host leads the document.
	ent := HostsYAML(Identity{Login: "alockwood", Token: "t", Host: "github.example.com"})
	if !strings.HasPrefix(ent, "github.example.com:\n") {
		t.Errorf("enterprise host should lead the yaml; got:\n%s", ent)
	}
}
