package ledger

import (
	"os"
	"path/filepath"
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
