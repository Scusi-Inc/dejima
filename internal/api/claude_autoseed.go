package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aoos/dejima/internal/agentcreds"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/project"
)

// Claude credential auto-seed — capture the operator's first in-island Claude
// login and store it host-side so every FUTURE island inherits it with no
// sign-in prompt. It automates what `dejima auth push` does by hand: an in-island
// login only writes ~/.claude/.credentials.json in THAT island's home volume, so
// today it's never propagated and each new island re-prompts.
//
// Guardrails (design reviewed):
//   - OWNER GATE: capture only from the operator's OWN islands (Owner ==
//     HostOwner) — a teammate/guest island's login can never become the host seed.
//   - NO CLOBBER: capture only while the host is UNSEEDED; never overwrite a login
//     the operator pushed themselves. This also makes it self-disabling.
//   - VALIDATE: only a well-formed Claude credentials blob is seeded.
//   - SURFACE, NOT SILENT: a ledger entry + a TypeCredentialsAutoSeeded event NAME
//     the source island, so a capture is always visible to the operator.
//
// KNOWN LIMITATION (accepted tradeoff for the solo/trusted-team target): the
// owner gate proves the ISLAND is owner-owned, but an agent can write its own
// ~/.claude/.credentials.json, so a compromised agent in the operator's own
// island could PLANT a credential and get it auto-seeded before the operator's
// first real login (ValidateClaude only checks well-formedness, not provenance).
// The window is bounded — it closes the moment any real login seeds the host —
// and every capture is surfaced with its source island, so a surprise is
// obvious. We deliberately favor zero-friction coverage + visibility over an
// attach-gate (which would miss legitimate captures where the operator logs in
// and detaches). Tightening this (attester/attach-proof) is future work.

// claudeCredInIsland is where an interactive Claude login writes its OAuth blob
// inside an island's home volume.
const claudeCredInIsland = "/home/dejima/.claude/.credentials.json"

// autoSeedSweepInterval is how often the backstop sweep looks for a capturable
// login while the host is unseeded. Self-disabling, so this only runs during the
// brief pre-seed window.
const autoSeedSweepInterval = 45 * time.Second

// autoSeedState guards the one-shot capture: done flips true once we've seeded
// (or confirmed the host is already seeded) so the steady-state path is a single
// cheap bool check with no container I/O.
type autoSeedState struct {
	mu   sync.Mutex
	done bool
}

func (s *Server) autoSeedDone() bool {
	s.autoSeed.mu.Lock()
	defer s.autoSeed.mu.Unlock()
	return s.autoSeed.done
}

func (s *Server) markAutoSeedDone() {
	s.autoSeed.mu.Lock()
	s.autoSeed.done = true
	s.autoSeed.mu.Unlock()
}

// hostClaudeSeeded reports whether new islands already get a Claude credential
// without help — a host login source (macOS Keychain / ~/.claude file) OR an
// existing materialized seed. Mirrors handleClaudeCredsStatus so the auto-seed
// gate matches the readiness the operator sees in `dejima auth status`.
func (s *Server) hostClaudeSeeded() bool {
	if _, _, err := agentcreds.LoadClaude(); err == nil {
		return true
	}
	if dir, err := paths.ClaudeSeedDir(); err == nil {
		if _, statErr := os.Stat(filepath.Join(dir, ".credentials.json")); statErr == nil {
			return true
		}
	}
	return false
}

// tryAutoSeedClaude is the cheap event-driven trigger: once we're done it's a
// single bool check with no allocation; otherwise it captures in the background
// so the agent-event path is never blocked on container I/O.
func (s *Server) tryAutoSeedClaude(island string) {
	if island == "" || s.autoSeedDone() {
		return
	}
	go func() {
		p, err := project.Load(island)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.maybeAutoSeedClaudeFrom(ctx, p)
	}()
}

// maybeAutoSeedClaudeFrom performs the one-shot capture from island p, applying
// every guardrail. Best-effort and idempotent: any gate failure is a quiet no-op.
func (s *Server) maybeAutoSeedClaudeFrom(ctx context.Context, p *project.Project) {
	if s.autoSeedDone() {
		return
	}
	// OWNER GATE — only the operator's own islands.
	if !strings.EqualFold(strings.TrimSpace(p.Owner), project.HostOwner()) {
		return
	}
	// NO CLOBBER + self-disable — if the host is already seeded there's nothing to
	// do, ever again this boot.
	if s.hostClaudeSeeded() {
		s.markAutoSeedDone()
		return
	}

	// Serialize the capture so concurrent triggers (many agent-events, or an
	// event racing the sweep) don't double-exec or double-write. Re-check under
	// the lock.
	s.autoSeed.mu.Lock()
	defer s.autoSeed.mu.Unlock()
	if s.autoSeed.done {
		return
	}

	// Read the island's credentials file. An island without a login yet (the
	// common pre-login case) returns non-zero / empty — skip quietly.
	stdout, _, code, err := s.rt.Exec(ctx, p.ContainerName(), []string{"cat", claudeCredInIsland})
	if err != nil || code != 0 {
		return
	}
	blob := []byte(strings.TrimSpace(stdout))
	// VALIDATE — only seed a well-formed Claude credentials blob.
	if err := agentcreds.ValidateClaude(blob); err != nil {
		return
	}
	dir, err := paths.ClaudeSeedDir()
	if err != nil {
		return
	}
	if _, err := agentcreds.WriteSeed(dir, blob); err != nil {
		s.log.Warn("claude auto-seed: write seed", "island", p.Name, "err", err)
		return
	}
	s.autoSeed.done = true

	// SURFACE, NOT SILENT — name the source island in both the log/ledger and the
	// event so the operator always knows their login was captured and from where.
	s.log.Info("claude auto-seed: captured the operator's login; future islands inherit it",
		"source_island", p.Name)
	s.ledgerAppend(ledger.Entry{
		Type:   string(events.TypeCredentialsAutoSeeded),
		Island: p.Name,
		Actor:  "daemon",
		Detail: "auto-captured the operator's Claude login; new islands skip sign-in",
	})
	s.emit(events.Event{
		Type:    events.TypeCredentialsAutoSeeded,
		Island:  p.Name,
		Payload: map[string]any{"source_island": p.Name},
	})
}

// RunClaudeAutoSeed is the backstop sweep: while the host is unseeded, it
// periodically scans the operator's running islands for a capturable login. It
// exists because a skewed in-island shim can silently stop POSTing agent-events
// (the socket→TCP skew that killed heartbeats for ~18h is the cautionary tale) —
// without the sweep, those exact islands would never auto-seed and the operator
// is back to a manual `dejima auth push` with no signal why. Self-disabling: once
// seeded it's a single bool check per tick. Run it in its own goroutine; returns
// when ctx is cancelled.
func (s *Server) RunClaudeAutoSeed(ctx context.Context) {
	t := time.NewTicker(autoSeedSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.autoSeedDone() {
				continue
			}
			projects, err := project.List()
			if err != nil {
				continue
			}
			for _, p := range projects {
				if s.autoSeedDone() {
					break
				}
				if p.DesiredState != project.StateRunning {
					continue // only a running island can hold a live login
				}
				sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				s.maybeAutoSeedClaudeFrom(sctx, p)
				cancel()
			}
		}
	}
}
