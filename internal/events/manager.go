package events

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/paths"
)

// Subscription is a registered webhook.
type Subscription struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret,omitempty"`     // HMAC-signs payloads if non-empty
	Events    []Type    `json:"events,omitempty"`     // empty = all
	CreatedAt time.Time `json:"created_at"`
}

// Manager fans out events to subscribed webhooks.
type Manager struct {
	log     *slog.Logger
	mu      sync.RWMutex
	subs    map[string]Subscription
	httpc   *http.Client
}

// New constructs a Manager and loads any persisted subscriptions.
func New(log *slog.Logger) (*Manager, error) {
	m := &Manager{
		log:   log,
		subs:  map[string]Subscription{},
		httpc: &http.Client{Timeout: 10 * time.Second},
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// path on disk where subscriptions live.
func storePath() (string, error) {
	root, err := paths.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "webhooks.json"), nil
}

func (m *Manager) load() error {
	path, err := storePath()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var list []Subscription
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range list {
		m.subs[s.ID] = s
	}
	return nil
}

func (m *Manager) save() error {
	path, err := storePath()
	if err != nil {
		return err
	}
	m.mu.RLock()
	list := make([]Subscription, 0, len(m.subs))
	for _, s := range m.subs {
		list = append(list, s)
	}
	m.mu.RUnlock()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Subscribe adds a webhook subscription. The returned Subscription includes
// a generated ID.
func (m *Manager) Subscribe(s Subscription) (Subscription, error) {
	if s.URL == "" {
		return Subscription{}, fmt.Errorf("url is required")
	}
	if s.ID == "" {
		s.ID = randomID(16)
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	m.subs[s.ID] = s
	m.mu.Unlock()
	if err := m.save(); err != nil {
		return Subscription{}, err
	}
	return s, nil
}

// Unsubscribe removes a subscription by ID.
func (m *Manager) Unsubscribe(id string) error {
	m.mu.Lock()
	delete(m.subs, id)
	m.mu.Unlock()
	return m.save()
}

// List returns every subscription.
func (m *Manager) List() []Subscription {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Subscription, 0, len(m.subs))
	for _, s := range m.subs {
		out = append(out, s)
	}
	return out
}

// Emit fans an event out to all matching subscribers. Non-blocking.
func (m *Manager) Emit(e Event) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	m.mu.RLock()
	targets := make([]Subscription, 0, len(m.subs))
	for _, s := range m.subs {
		if matches(s.Events, e.Type) {
			targets = append(targets, s)
		}
	}
	m.mu.RUnlock()

	for _, s := range targets {
		go m.deliver(s, e)
	}
}

func matches(filter []Type, t Type) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if f == t {
			return true
		}
	}
	return false
}

func (m *Manager) deliver(s Subscription, e Event) {
	data, err := json.Marshal(e)
	if err != nil {
		m.log.Warn("event marshal", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.URL, bytes.NewReader(data))
	if err != nil {
		m.log.Warn("event request", "url", s.URL, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Dejima-Event", string(e.Type))
	if s.Secret != "" {
		req.Header.Set("X-Dejima-Signature", sign(s.Secret, data))
	}
	resp, err := m.httpc.Do(req)
	if err != nil {
		m.log.Warn("event delivery failed", "url", s.URL, "type", e.Type, "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		m.log.Warn("event delivery non-2xx", "url", s.URL, "type", e.Type, "status", resp.StatusCode)
	}
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func randomID(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
