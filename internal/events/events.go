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
	TypePanicEngaged       Type = "daemon.panic-engaged"
	TypePanicCleared       Type = "daemon.panic-cleared"
	TypeClientAttached     Type = "client.attached"
	TypeClientDetached     Type = "client.detached"
	TypeLastClientDetached Type = "last-client.detached"

	// Agent-emitted events (via per-agent shims; opt-in).
	TypeAgentWaitingForInput Type = "agent.waiting-for-input"
	TypeAgentTaskComplete    Type = "agent.task-complete"
	TypeAgentError           Type = "agent.error"
)

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
