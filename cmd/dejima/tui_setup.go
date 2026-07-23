package main

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// setupReadinessMsg is the one-shot snapshot of "can a new island actually run
// agents?" — whether the daemon can seed Claude credentials, and which agent
// types need an LLM provider key that isn't configured. Fetched once at Init so
// the dashboard and the new-island creator can warn BEFORE an island exists,
// instead of the user discovering it when an agent fails at first attach.
type setupReadinessMsg struct {
	claudeSeeded bool
	keyGap       map[string]bool // agent type → requires a provider key, none set for it
	gatewayPort  map[string]int  // agent type → its localhost gateway port (0/absent = none)
}

// fetchSetupReadinessCmd loads the credential/provider-key picture in one go.
// Best-effort: any sub-fetch that fails (e.g. an older daemon without an
// endpoint) just leaves that part optimistic, so we never warn spuriously.
func (m tuiModel) fetchSetupReadinessCmd() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Claude credentials: missing only when there's no host login AND no
		// pushed seed — mirrors `dejima doctor`'s checkClaudeCreds verdict.
		msg := setupReadinessMsg{claudeSeeded: true, keyGap: map[string]bool{}, gatewayPort: map[string]int{}}
		if st, err := c.ClaudeCredentialsStatus(ctx); err == nil {
			msg.claudeSeeded = st.SeedPresent || st.HostSource != ""
		}

		// Provider keys: which key-requiring agent types have no usable key.
		types, err := c.ListAgentTypes(ctx)
		if err != nil {
			return msg
		}
		keySet := map[string]bool{}
		if creds, err := c.ListProviderCredentials(ctx); err == nil {
			for _, p := range creds {
				if p.KeySet {
					keySet[p.Name] = true
				}
			}
		}
		for _, t := range types {
			if !t.RequiresProviderKey {
				continue
			}
			have := false
			if len(t.SupportedProviders) == 0 {
				have = len(keySet) > 0 // no advisory list → any configured key counts
			} else {
				for _, p := range t.SupportedProviders {
					if keySet[p] {
						have = true
						break
					}
				}
			}
			if !have {
				msg.keyGap[t.Type] = true
			}
		}
		return msg
	}
}
