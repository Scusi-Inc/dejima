// Package ledger is the append-only, hash-chained audit log for brokered
// operations — Port scope grants and file Trades. It lives host-side at
// ~/.dejima/ledger.jsonl, outside every container's blast radius, so a
// compromised island cannot rewrite its own history.
//
// Each entry chains to the previous one via a SHA-256 (or HMAC-SHA-256, if a
// key is configured) hash over the previous chain value plus the entry's own
// content. Any in-place edit or deletion of a past entry breaks the chain and
// is detectable by Verify. This is the substrate the Port spec requires before
// an island may be granted host-file access (docs/port-island-spec.md §4).
package ledger

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/paths"
)

// Entry is one append-only record. Trade entries (file crossings) carry Bytes
// and SHA256; scope entries (grant/revoke) carry Scope/Path/Mode; operational
// audit entries (api.request + lifecycle) carry Method/Status/Actor/Role. Prev
// and Chain are filled by Append and must not be set by callers.
//
// Every field beyond the chain bookkeeping is `omitempty`, which is what lets a
// new build add fields without breaking the verification of entries written by
// an older build: chainValue re-marshals the parsed Entry, and an absent field
// marshals identically whether the writer knew about it or not. Do NOT drop
// omitempty on an existing or new field — it would silently break every chain.
// Provenance says HOW Dejima knows an entry is true. Three genuinely different
// claims, and presenting them identically is how a strong one gets inherited by
// a weak one.
//
//	brokered      the daemon ALLOWED the action    → "this was allowed"
//	witnessed     the daemon SAW it happen         → "this happened"
//	self-reported the agent said it happened       → "the agent reported this"
//
// The third is the dangerous one. An observed agent — or anything that has
// compromised it — can write whatever it likes, and omission is trivial: do not
// write the line. "Here is an audit trail" and "here is an audit trail, some
// rows of which are the subject's own account of itself" are different products.
//
// THE ZERO VALUE IS "" AND MEANS UNKNOWN, NOT BROKERED. Same rule as
// ContainmentLevel: the zero must never be the reassuring answer. In practice ""
// appears only on entries written before this field existed — which are brokered
// in fact, because self-reporting did not exist then — but that reasoning rots
// the moment a new writer forgets to stamp, so the record says unknown and the
// reader is not invited to assume.
//
// NAMED "witnessed", NOT "observed", DELIBERATELY. `observed` is already the
// containment level for an UNGATED AGENT (api.ContainmentObserved), and a
// provenance meaning "the daemon saw a contained action happen" would be the
// second collision of exactly the kind d1 just ruled on for adopt/observe. One
// word, two meanings, on the axis the product is about.
type Provenance string

const (
	// ProvenanceBrokered — the daemon made the decision and the action went
	// through it. The strongest claim, and the one every existing Port/Trade row
	// is entitled to.
	ProvenanceBrokered Provenance = "brokered"
	// ProvenanceWitnessed — the daemon saw a contained action happen without
	// gating it. Weaker than brokered, stronger than self-reported: Dejima's own
	// observation of something inside containment.
	ProvenanceWitnessed Provenance = "witnessed"
	// ProvenanceSelfReported — the subject's own account of itself, tailed from
	// something the agent wrote. Dejima did not see it and cannot verify it.
	ProvenanceSelfReported Provenance = "self-reported"
)

// Verified reports whether Dejima itself is the source of this claim. Written as
// a method so the cautious reading lives in ONE place: `!= self-reported`
// scattered across renderers is how one of them ends up treating the unknown
// zero value as trustworthy.
func (p Provenance) Verified() bool {
	return p == ProvenanceBrokered || p == ProvenanceWitnessed
}

type Entry struct {
	Seq        uint64    `json:"seq"`
	Time       time.Time `json:"time"`
	Type       string    `json:"type"` // port.grant | port.revoke | trade.read | trade.write | trade.deny | api.request | <lifecycle event>
	Island     string    `json:"island"`
	Agent      string    `json:"agent,omitempty"`       // agent id — the machine-keyed lookup handle
	AgentLabel string    `json:"agent_label,omitempty"` // agent's human-given name, for read-side display (#190 label(id)); Agent stays the id
	Scope      string    `json:"scope,omitempty"`       // scope name
	Path       string    `json:"path,omitempty"`        // host path (scope) or path within scope (trade); request path (api.request)
	Mode       string    `json:"mode,omitempty"`        // ro | rw
	Bytes      int64     `json:"bytes,omitempty"`
	SHA256     string    `json:"sha256,omitempty"`   // content hash of the file (trades)
	Decision   string    `json:"decision,omitempty"` // allowed | denied
	// Provenance says HOW Dejima knows this entry is true. See the Provenance
	// type — the short version is that a self-reported row and a brokered row
	// must never be indistinguishable, because the integrity claim of the whole
	// ledger degrades to that of its weakest row.
	Provenance Provenance `json:"provenance,omitempty"`
	Detail     string     `json:"detail,omitempty"`
	// Operational audit fields (api.request + lifecycle records).
	Method string `json:"method,omitempty"` // HTTP method (api.request)
	Status int    `json:"status,omitempty"` // HTTP status code (api.request)
	Actor  string `json:"actor,omitempty"`  // who made the request (identity; filled by the auth layer)
	Role   string `json:"role,omitempty"`   // the actor's role, when known
	Prev   string `json:"prev"`             // chain value of the previous entry ("" for the first)
	Chain  string `json:"chain"`            // chain value of this entry
}

// Log is an append-only ledger backed by a single JSONL file.
type Log struct {
	mu      sync.Mutex
	path    string
	hmacKey []byte // optional; nil => plain SHA-256 chain

	loaded    bool
	lastSeq   uint64
	lastChain string
}

// New returns a Log backed by path. If hmacKey is non-nil the chain is an
// HMAC-SHA-256 keyed by it; otherwise it is a plain SHA-256 chain.
func New(path string, hmacKey []byte) *Log {
	return &Log{path: path, hmacKey: hmacKey}
}

var (
	defaultMu      sync.Mutex
	defaultLog     *Log
	defaultErr     error
	defaultSet     bool
	defaultHMACKey []byte // optional; keys the Default chain when non-empty
)

// Configure sets process-wide options for the Default ledger. It must be called
// before the first Default() use (e.g. at daemon startup, before any append).
//
// A non-empty hmacKey keys the chain with HMAC-SHA-256 instead of plain
// SHA-256, so the hash chain can only be re-derived by a holder of the key —
// raising the bar from "tamper is detectable" to "tamper requires the key".
// The whole file must use one keying, so this is meaningful only on a fresh
// ledger: turning HMAC on over a file that already holds plain-SHA entries will
// make Verify report those older entries as broken. Calling after the Default
// log is already built is a no-op (the keying is fixed for the daemon's life).
func Configure(hmacKey []byte) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultSet {
		return
	}
	if len(hmacKey) > 0 {
		defaultHMACKey = append([]byte(nil), hmacKey...)
	} else {
		defaultHMACKey = nil
	}
}

// Default returns the process-wide ledger at ~/.dejima/ledger.jsonl. A single
// daemon owns the file, so one shared Log keeps the in-memory chain head
// consistent across concurrent appends. The path is resolved (from $HOME) on
// first use and cached; ResetDefault drops the cache. The chain is keyed by the
// optional HMAC key set via Configure.
func Default() (*Log, error) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if !defaultSet {
		if p, err := paths.LedgerPath(); err != nil {
			defaultErr = err
		} else {
			defaultLog = New(p, defaultHMACKey)
		}
		defaultSet = true
	}
	return defaultLog, defaultErr
}

// ResetDefault drops the cached process-wide ledger so the next Default()
// re-resolves its path from $HOME (and re-reads the Configure'd HMAC key). For
// tests that redirect HOME between cases; not for production use (the daemon's
// HOME is fixed for its lifetime).
func ResetDefault() {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultLog, defaultErr, defaultSet = nil, nil, false
	defaultHMACKey = nil
}

// Append seals e onto the chain and writes it as one JSONL line. It assigns
// Seq, Time (if zero), Prev, and Chain, and returns the sealed entry.
func (l *Log) Append(e Entry) (Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded {
		if err := l.load(); err != nil {
			return Entry{}, err
		}
	}
	e.Seq = l.lastSeq + 1
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	e.Prev = l.lastChain
	e.Chain = l.chainValue(e)

	line, err := json.Marshal(e)
	if err != nil {
		return Entry{}, err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return Entry{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return Entry{}, err
	}
	if err := f.Close(); err != nil {
		return Entry{}, err
	}
	l.lastSeq = e.Seq
	l.lastChain = e.Chain
	return e, nil
}

// chainValue computes the chain hash for e, ignoring any Chain already set.
func (l *Log) chainValue(e Entry) string {
	e.Chain = ""
	payload, _ := json.Marshal(e)
	msg := append([]byte(e.Prev+"\n"), payload...)
	if len(l.hmacKey) > 0 {
		m := hmac.New(sha256.New, l.hmacKey)
		m.Write(msg)
		return hex.EncodeToString(m.Sum(nil))
	}
	sum := sha256.Sum256(msg)
	return hex.EncodeToString(sum[:])
}

// Read returns every entry in order. Missing file => empty slice.
func (l *Log) Read() ([]Entry, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("ledger: corrupt entry: %w", err)
		}
		out = append(out, e)
	}
	return out, nil
}

// Verify walks the whole chain and reports the first broken link, if any.
func (l *Log) Verify() error {
	entries, err := l.Read()
	if err != nil {
		return err
	}
	prev := ""
	for _, e := range entries {
		if e.Prev != prev {
			return fmt.Errorf("ledger: entry %d prev mismatch (chain broken — entry inserted or removed)", e.Seq)
		}
		if got := l.chainValue(e); got != e.Chain {
			return fmt.Errorf("ledger: entry %d chain mismatch (entry tampered)", e.Seq)
		}
		prev = e.Chain
	}
	return nil
}

// load seeds lastSeq/lastChain from the tail of an existing file.
func (l *Log) load() error {
	data, err := os.ReadFile(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			l.loaded = true
			return nil
		}
		return err
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(lines[i], &e); err != nil {
			return fmt.Errorf("ledger: corrupt tail entry: %w", err)
		}
		l.lastSeq = e.Seq
		l.lastChain = e.Chain
		break
	}
	l.loaded = true
	return nil
}
