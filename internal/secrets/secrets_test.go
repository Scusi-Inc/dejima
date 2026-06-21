package secrets

import "testing"

func TestFileStoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate ~/.dejima

	fs, err := newFileStore()
	if err != nil {
		t.Fatalf("newFileStore: %v", err)
	}
	if _, ok, err := fs.get("missing"); err != nil || ok {
		t.Fatalf("get(missing) = ok=%v err=%v, want false/nil", ok, err)
	}
	if err := fs.set("hmac", "s3cr3t"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, ok, _ := fs.get("hmac"); !ok || v != "s3cr3t" {
		t.Fatalf("get = %q ok=%v, want s3cr3t/true", v, ok)
	}
	if err := fs.set("hmac", "rotated"); err != nil { // update in place
		t.Fatalf("update: %v", err)
	}
	if v, _, _ := fs.get("hmac"); v != "rotated" {
		t.Fatalf("get after update = %q, want rotated", v)
	}
	if err := fs.del("hmac"); err != nil {
		t.Fatalf("del: %v", err)
	}
	if _, ok, _ := fs.get("hmac"); ok {
		t.Fatal("get after del still found the secret")
	}
	if err := fs.del("missing"); err != nil { // deleting a missing key is a no-op
		t.Fatalf("del(missing): %v", err)
	}
}

// TestStoreFallsBackToFile exercises the Store with no keychain backend (the
// headless / locked-keychain degradation), so it must route to the file store.
func TestStoreFallsBackToFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs, err := newFileStore()
	if err != nil {
		t.Fatalf("newFileStore: %v", err)
	}
	s := &Store{kc: nil, file: fs}
	if s.Backend() != "file" {
		t.Errorf("Backend() = %q, want file", s.Backend())
	}
	if err := s.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, ok, _ := s.Get("k"); !ok || v != "v" {
		t.Errorf("Get = %q ok=%v, want v/true", v, ok)
	}
	if err := s.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := s.Get("k"); ok {
		t.Error("Get after Delete still found the secret")
	}
}
