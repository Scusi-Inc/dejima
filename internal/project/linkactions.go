package project

import "strings"

// ExposesAction reports whether this island exposes the named action type for
// cross-island delegation (Lane 5, Phase 3). Deny-all: an unexposed action can
// never be invoked, even with a link grant.
func (p *Project) ExposesAction(action string) bool {
	action = strings.TrimSpace(action)
	for _, a := range p.LinkActions {
		if a == action {
			return true
		}
	}
	return false
}

// AddLinkAction exposes action (idempotent). Returns false if already exposed.
func (p *Project) AddLinkAction(action string) bool {
	action = strings.TrimSpace(action)
	if action == "" || p.ExposesAction(action) {
		return false
	}
	p.LinkActions = append(p.LinkActions, action)
	return true
}

// RemoveLinkAction unexposes action. Returns false if it wasn't exposed.
func (p *Project) RemoveLinkAction(action string) bool {
	action = strings.TrimSpace(action)
	for i, a := range p.LinkActions {
		if a == action {
			p.LinkActions = append(p.LinkActions[:i], p.LinkActions[i+1:]...)
			return true
		}
	}
	return false
}
