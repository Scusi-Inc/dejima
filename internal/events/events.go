// Package events defines the Dejima event model and the webhook delivery
// manager. Events are emitted by the daemon (lifecycle, container, presence)
// and by per-agent shims via the internal agent-event endpoint.
package events

import "time"

// Type is a stable event identifier suitable for subscriber filtering.
type Type string

// Daemon-observable events (always agnostic).
const (
	TypeIslandCreated      Type = "island.created"
	TypeIslandRunning      Type = "island.running"
	TypeIslandHibernated   Type = "island.hibernated"
	TypeIslandWoken        Type = "island.woken"
	TypeIslandReset        Type = "island.reset"
	TypeIslandUpgraded     Type = "island.upgraded"
	TypeIslandPurged       Type = "island.purged"
	TypeIslandAgentAdded   Type = "island.agent-added"
	TypeIslandAgentRemoved Type = "island.agent-removed"
	TypeContainerCrashed   Type = "container.crashed"
	TypeDaemonStarted      Type = "daemon.started"
	TypePanicEngaged       Type = "daemon.panic-engaged"
	TypePanicCleared       Type = "daemon.panic-cleared"
	TypeClientAttached     Type = "client.attached"
	TypeClientDetached     Type = "client.detached"
	TypeLastClientDetached Type = "last-client.detached"

	// Agent-emitted events (via per-agent shims; opt-in).
	TypeAgentWaitingForInput Type = "agent.waiting-for-input"
	TypeAgentTaskComplete    Type = "agent.task-complete"
	TypeAgentError           Type = "agent.error"
	// TypeAgentUsage carries an adapter's self-reported token usage (Dejima can't
	// see the opaque LLM call). Payload: input_tokens / cache_creation_input_tokens
	// / cache_read_input_tokens / output_tokens / model / source. Ingested into the
	// per-agent usage snapshot surfaced on AgentInfo.Usage.
	TypeAgentUsage Type = "agent.usage"

	// Inter-island action delegation (Lane 5, Phase 3): a cross-island action
	// needs operator approval. Fires so an operator can approve/deny from a phone
	// notification; the payload carries from/to islands + the action type.
	TypeLinkActionPending Type = "link.action-pending"

	// Mailbox arrival (Lane 5, Phase 3.5): a message landed in an agent's mailbox.
	// The wake-on-message seam — Dejima's default soft-notify acts on it, and a
	// wrapper can subscribe to implement its own routing/priority. Payload carries
	// island/agent and cross_island/action flags (never the message body).
	TypeMailboxArrival Type = "mailbox.arrival"
)

// catalog is every event type a subscriber can filter on, in display order.
// Kept in sync with the constants above; KnownType validates against it so a
// typo'd subscription filter (which would silently match nothing) is rejected.
var catalog = []Type{
	TypeIslandCreated, TypeIslandRunning, TypeIslandHibernated, TypeIslandWoken,
	TypeIslandReset, TypeIslandUpgraded, TypeIslandPurged,
	TypeIslandAgentAdded, TypeIslandAgentRemoved,
	TypeContainerCrashed, TypeDaemonStarted, TypePanicEngaged, TypePanicCleared,
	TypeClientAttached, TypeClientDetached, TypeLastClientDetached,
	TypeAgentWaitingForInput, TypeAgentTaskComplete, TypeAgentError, TypeAgentUsage,
	TypeLinkActionPending,
}

// KnownTypes returns the catalog of subscribable event types (a copy).
func KnownTypes() []Type {
	out := make([]Type, len(catalog))
	copy(out, catalog)
	return out
}

// KnownType reports whether t is a recognized event type.
func KnownType(t Type) bool {
	for _, k := range catalog {
		if k == t {
			return true
		}
	}
	return false
}

// Event is the JSON envelope POSTed to webhook subscribers.
type Event struct {
	Type   Type   `json:"type"`
	Island string `json:"island,omitempty"`
	// Agent is the agent within the island this event concerns (empty for
	// island-level events or agents that don't identify themselves).
	Agent     string         `json:"agent,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}
