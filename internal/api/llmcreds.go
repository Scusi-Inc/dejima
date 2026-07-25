package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"encoding/json"

	"github.com/aoos/dejima/internal/handlers"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/providercreds"
)

// handleListProviderCreds returns the daemon's LLM provider credentials without
// their keys (masked hint only). Operator-only — absent from tokenRouteAccess,
// so an in-island token can't enumerate or read provider keys.
func (s *Server) handleListProviderCreds(w http.ResponseWriter, _ *http.Request) {
	store, err := providercreds.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ProviderCredentialsResponse{Providers: store.List()})
}

// handlePutProviderCred stores or updates a provider key (`dejima provider set`).
// The key is write-only; the response is the key-less list.
func (s *Server) handlePutProviderCred(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(r.PathValue("provider"))
	if provider == "" {
		writeError(w, http.StatusBadRequest, errors.New("provider name is required"))
		return
	}
	var req PutProviderCredentialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	if strings.TrimSpace(req.APIKey) == "" {
		writeError(w, http.StatusBadRequest, errors.New("api_key is required"))
		return
	}
	store, err := providercreds.Update(func(st *providercreds.Store) error {
		st.Put(providercreds.Provider{
			Name:    provider,
			APIKey:  req.APIKey,
			BaseURL: strings.TrimSpace(req.BaseURL),
			EnvVar:  strings.TrimSpace(req.EnvVar),
		})
		if req.Default {
			_ = st.SetDefault(provider)
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Log the provider name only — never the key.
	s.log.Info("provider credential stored", "provider", provider)
	writeJSON(w, http.StatusOK, ProviderCredentialsResponse{Providers: store.List()})
}

// handleDeleteProviderCred removes a provider credential and reports which
// islands still reference it (their agents read missing-provider-auth until
// reconfigured).
func (s *Server) handleDeleteProviderCred(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	var missing bool
	if _, err := providercreds.Update(func(st *providercreds.Store) error {
		missing = !st.Remove(provider)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if missing {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such provider %q", provider))
		return
	}
	affected := s.islandsUsingProvider(provider)
	if len(affected) > 0 {
		s.log.Warn("deleted a provider credential still referenced by islands",
			"provider", provider, "islands", affected)
	}
	writeJSON(w, http.StatusOK, DeleteProviderCredentialResponse{AffectedIslands: affected})
}

// islandsUsingProvider returns the islands whose agents resolve to the named
// provider — explicitly, or via the store default when the agent leaves it
// blank. Best-effort: a project-list failure yields no names.
func (s *Server) islandsUsingProvider(name string) []string {
	projects, err := project.List()
	if err != nil {
		return nil
	}
	store, _ := providercreds.Load()
	isDefault := store != nil && store.Default == name
	var out []string
	for _, p := range projects {
		p.EnsureAgents()
		for i := range p.Agents {
			a := &p.Agents[i]
			if h, ok := handlers.Lookup(a.Type); !ok || !h.RequiresProviderKey {
				continue
			}
			prov := strings.TrimSpace(a.Provider)
			if prov == name || (prov == "" && isDefault) {
				out = append(out, p.Name)
				break
			}
		}
	}
	return out
}

// handleAgentTypes returns the capability descriptors for the built-in agent
// types — what drives a client's provider/model picker and the channels
// affordance. Readable by in-island tokens too (a brain may discover its own
// capabilities); it carries no secrets.
func (s *Server) handleAgentTypes(w http.ResponseWriter, _ *http.Request) {
	all := handlers.All()
	out := make([]AgentTypeCapability, 0, len(all))
	for _, h := range all {
		out = append(out, AgentTypeCapability{
			Type:                h.ID,
			Interactive:         h.Attachable(),
			RequiresProviderKey: h.RequiresProviderKey,
			SupportedProviders:  h.SupportedProviders,
			SuggestedModels:     h.SuggestedModels,
			GatewayPort:         h.GatewayPort,
			DashboardTokenCmd:    h.DashboardTokenCmd,
			DashboardTokenSuffix: h.DashboardTokenSuffix,
		})
	}
	writeJSON(w, http.StatusOK, AgentTypesResponse{Types: out})
}

// configureAgent sets an agent's LLM provider/model. Operator-only: a contained
// brain must not rebind its own model/key. A change needs a container recreate to
// take effect (the selection rides create-time env), so the response flags
// RestartRequired — reusing the resources recreate-to-apply pattern.
func (s *Server) configureAgent(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("name"), r.PathValue("id")
	lock := s.projectLock(name)
	lock.Lock()
	defer lock.Unlock()

	p, err := project.Load(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	a, ok := p.AgentByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("island %q has no agent %q", name, id))
		return
	}
	if h, ok := handlers.Lookup(a.Type); !ok || !h.RequiresProviderKey {
		writeError(w, http.StatusConflict,
			fmt.Errorf("agent %q (type %q) does not use an LLM provider key", id, a.Type))
		return
	}
	var req AgentConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err))
		return
	}
	changed := false
	if req.Provider != nil {
		if v := strings.TrimSpace(*req.Provider); v != a.Provider {
			a.Provider = v
			changed = true
		}
	}
	if req.Model != nil {
		if v := normalizeModel(a.Provider, *req.Model); v != a.Model {
			a.Model = v
			changed = true
		}
	}
	// Warn (don't fail) when the chosen provider has no stored key — a base_url
	// proxy may legitimately remap, and the missing-key state is surfaced by
	// agent health anyway.
	if a.Provider != "" {
		if store, err := providercreds.Load(); err == nil {
			if _, ok := store.Resolve(a.Provider); !ok {
				s.log.Warn("agent configured for a provider with no stored key",
					"island", name, "agent", id, "provider", a.Provider)
			}
		}
	}
	if err := p.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, AgentConfigResponse{
		Provider:        a.Provider,
		Model:           a.Model,
		RestartRequired: changed,
	})
}
