// Package provision implements `dejima onboard --provision-host`: a resumable
// detect→act→verify state machine that turns a fresh Mac mini or Linux box into
// a secure Dejima agent host, ending by handing off to the existing service
// install flow. Each phase detects whether it's already satisfied, acts (running
// a command or instructing a manual step), and verifies before advancing;
// progress persists so an interrupted run resumes.
package provision

import (
	"fmt"
	"io"
)

// Status is the outcome of a phase's Detect.
type Status int

const (
	// StatusDone means the phase is already satisfied — skip Act/Verify.
	StatusDone Status = iota
	// StatusNeedsAct means the phase must run Act then Verify.
	StatusNeedsAct
	// StatusSkip means the phase doesn't apply on this host (recorded, skipped).
	StatusSkip
)

// Phase is one detect→act→verify step of host provisioning.
type Phase interface {
	// ID is the stable key persisted in state (e.g. "tooling"). Must be unique.
	ID() string
	// Title is the human label shown in the wizard.
	Title() string
	// Detect reports whether the phase is satisfied, needs action, or doesn't
	// apply, with a human detail line.
	Detect(*Context) (Status, string, error)
	// Act performs the phase: runs commands and/or instructs manual steps.
	Act(*Context) error
	// Verify confirms Act succeeded. Only a true result advances the wizard.
	Verify(*Context) (bool, string, error)
}

// Context is the shared state threaded through every phase.
type Context struct {
	OS      string // runtime.GOOS, overridable in tests
	Yes     bool   // non-interactive: auto-confirm automatable steps, skip GUI ones
	SelfBin string // path to this dejima binary, for install/verify handoff steps
	State   *State
	Runner  Runner
	Out     io.Writer
	In      io.Reader

	// DisableSSHPassword opts into the security-consequential SSH
	// password-disable step. Separate from Yes — `--yes` must never imply it.
	DisableSSHPassword bool
}

// answer reads a persisted answer (e.g. an opt-in decision), with a default.
func (c *Context) answer(key string, def any) any {
	if v, ok := c.State.Answers[key]; ok {
		return v
	}
	return def
}

// setAnswer records a decision so re-runs don't re-prompt.
func (c *Context) setAnswer(key string, v any) { c.State.Answers[key] = v }

// Engine walks a phase list, persisting progress after each transition.
type Engine struct {
	ctx *Context
}

// NewEngine builds an engine over a Context.
func NewEngine(ctx *Context) *Engine { return &Engine{ctx: ctx} }

// Run executes phases in order. A phase already in CompletedPhases is skipped.
// Otherwise Detect runs: StatusDone marks it complete; StatusSkip records it;
// StatusNeedsAct runs Act then Verify, and only a passing Verify marks it
// complete. A failed Act/Verify sets CurrentPhase, persists, and returns a
// non-nil error so a re-run resumes here. State is saved after every transition.
func (e *Engine) Run(phases []Phase) error {
	c := e.ctx
	for _, p := range phases {
		if c.State.IsComplete(p.ID()) {
			c.say("✓ %s (already done)", p.Title())
			continue
		}
		c.say("\n▸ %s", p.Title())

		status, detail, err := p.Detect(c)
		if err != nil {
			return e.halt(p, "detect", err)
		}
		switch status {
		case StatusDone:
			if detail != "" {
				c.say("  %s", detail)
			}
			c.State.markComplete(p.ID())
			_ = c.State.Save()
			continue
		case StatusSkip:
			c.say("  skipped: %s", detail)
			c.State.markSkipped(p.ID())
			c.State.markComplete(p.ID())
			_ = c.State.Save()
			continue
		}
		if detail != "" {
			c.say("  %s", detail)
		}

		c.State.CurrentPhase = p.ID()
		_ = c.State.Save()

		if err := p.Act(c); err != nil {
			return e.halt(p, "act", err)
		}
		ok, vdetail, verr := p.Verify(c)
		if verr != nil {
			return e.halt(p, "verify", verr)
		}
		if !ok {
			return e.halt(p, "verify", fmt.Errorf("%s", vdetail))
		}
		if vdetail != "" {
			c.say("  ✓ %s", vdetail)
		}
		c.State.markComplete(p.ID())
		_ = c.State.Save()
	}
	return nil
}

// halt records the stalled phase, persists, and returns an error that tells the
// operator a re-run resumes here.
func (e *Engine) halt(p Phase, stage string, err error) error {
	e.ctx.State.CurrentPhase = p.ID()
	_ = e.ctx.State.Save()
	e.ctx.say("  ✗ %s failed at %s: %v", p.Title(), stage, err)
	e.ctx.say("  fix the above and re-run `dejima onboard --provision-host` to resume here.")
	return fmt.Errorf("provision halted at %q (%s): %w", p.ID(), stage, err)
}
