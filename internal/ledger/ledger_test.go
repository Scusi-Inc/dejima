package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendChainsAndVerifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	l := New(path, nil)

	first, err := l.Append(Entry{Type: "port.grant", Island: "myrepo", Scope: "vault", Path: "/data/vault", Mode: "ro", Decision: "allowed"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if first.Seq != 1 || first.Prev != "" || first.Chain == "" {
		t.Fatalf("first entry: seq=%d prev=%q chain=%q", first.Seq, first.Prev, first.Chain)
	}
	second, err := l.Append(Entry{Type: "trade.read", Island: "myrepo", Scope: "vault", Path: "note.md", Bytes: 12})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if second.Seq != 2 || second.Prev != first.Chain {
		t.Fatalf("second entry not chained: prev=%q want=%q", second.Prev, first.Chain)
	}
	if err := l.Verify(); err != nil {
		t.Fatalf("verify clean chain: %v", err)
	}
}

func TestVerifyDetectsTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	l := New(path, nil)
	if _, err := l.Append(Entry{Type: "trade.read", Island: "x", Path: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(Entry{Type: "trade.read", Island: "x", Path: "b"}); err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the first record's path.
	data, _ := os.ReadFile(path)
	for i := range data {
		if data[i] == 'a' {
			data[i] = 'z'
			break
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(); err == nil {
		t.Fatal("expected tamper to be detected, got nil")
	}
}

func TestLoadResumesChainAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	a := New(path, nil)
	e1, _ := a.Append(Entry{Type: "port.grant", Island: "x", Scope: "s"})

	// A fresh Log over the same file must continue the chain, not restart it.
	b := New(path, nil)
	e2, err := b.Append(Entry{Type: "trade.read", Island: "x", Scope: "s"})
	if err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if e2.Seq != e1.Seq+1 {
		t.Fatalf("seq did not resume: got %d after %d", e2.Seq, e1.Seq)
	}
	if e2.Prev != e1.Chain {
		t.Fatalf("chain did not resume: prev=%q want=%q", e2.Prev, e1.Chain)
	}
	if err := b.Verify(); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

func TestHMACChainKeyingMatters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	key := []byte("super-secret-audit-key")

	keyed := New(path, key)
	if _, err := keyed.Append(Entry{Type: "api.request", Island: "x", Method: "POST", Path: "/v1/islands", Status: 201}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := keyed.Append(Entry{Type: "island.created", Island: "x"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// A fresh keyed Log over the same file verifies.
	if err := New(path, key).Verify(); err != nil {
		t.Fatalf("keyed verify should pass: %v", err)
	}
	// Without the key (or with the wrong key) the chain cannot be re-derived, so
	// verification fails — that's the whole point of HMAC keying.
	if err := New(path, nil).Verify(); err == nil {
		t.Fatal("unkeyed verify of a keyed chain should fail")
	}
	if err := New(path, []byte("wrong-key")).Verify(); err == nil {
		t.Fatal("wrong-key verify should fail")
	}
}

func TestOperationalEntryChains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.jsonl")
	l := New(path, nil)
	// Mix operational and brokered records on one chain.
	if _, err := l.Append(Entry{Type: "api.request", Island: "x", Method: "DELETE", Path: "/v1/islands/x", Status: 403, Actor: "operator", Role: "operator", Decision: "denied"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(Entry{Type: "port.grant", Island: "x", Scope: "vault", Path: "/data", Mode: "ro", Decision: "allowed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(Entry{Type: "island.purged", Island: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(); err != nil {
		t.Fatalf("mixed chain verify: %v", err)
	}
	entries, err := l.Read()
	if err != nil || len(entries) != 3 {
		t.Fatalf("read: %v len=%d", err, len(entries))
	}
}

// TestOmitemptyPreservesOldChains guards the invariant that lets a newer build
// add Entry fields without breaking entries written by an older build: an entry
// with no operational fields must marshal without those keys, so chainValue
// recomputes the exact bytes the old writer hashed.
func TestOmitemptyPreservesOldChains(t *testing.T) {
	b, err := json.Marshal(Entry{Type: "port.grant", Island: "x"})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"method", "status", "actor", "role", "scope", "bytes"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Fatalf("empty entry must omit %q; got %s", k, b)
		}
	}
}

func TestConfigureKeysDefaultLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".dejima"), 0o700); err != nil {
		t.Fatal(err)
	}
	key := []byte("default-ledger-key")

	ResetDefault()
	Configure(key)
	lg, err := Default()
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if _, err := lg.Append(Entry{Type: "daemon.started"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := lg.Verify(); err != nil {
		t.Fatalf("keyed default verify: %v", err)
	}
	// Configure after the default is built is a no-op (keying is fixed for life).
	Configure([]byte("late-key"))
	if err := lg.Verify(); err != nil {
		t.Fatalf("verify after late Configure: %v", err)
	}
	ResetDefault() // don't leak the keyed default into other tests
}
