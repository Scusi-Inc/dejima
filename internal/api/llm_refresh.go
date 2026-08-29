package api

import "github.com/aoos/dejima/internal/project"

// refreshIslandLLMConfigs re-materializes every island's LLM provider key files
// after the provider store changes.
//
// Third of the same shape (refreshIslandSecrets, refreshIslandGitHubConfigs).
// islandLLMConfigDir ran only from credentialBindMounts, at container CREATE, so
// a rotated provider key never reached an island that already existed: the
// operator updates the key, `dejima provider ls` shows the new one, and the
// agent keeps presenting the dead one — surfacing as "Authentication failed
// (provider returned HTTP 401)" with nothing pointing at the cause. Deletion was
// worse: the store forgot the key and the island kept a working copy.
//
// Mount is a directory, so a rewrite is visible to a RUNNING container. An
// already-launched agent still holds its start-time environment — the shim
// sources the .env at launch — so the agent must restart to pick up a rotation.
// That is a different fact from the mount being stale, and only this half was
// ever broken.
//
// Best-effort and per-island: one unreadable project must not stop the rest, and
// a failure is logged rather than returned because the store write ALREADY
// SUCCEEDED. Failing the request here would tell the operator their key was not
// stored when it was.
func (s *Server) refreshIslandLLMConfigs() {
	projects, err := project.List()
	if err != nil {
		s.log.Warn("could not list islands to refresh llm configs", "err", err)
		return
	}
	for _, p := range projects {
		s.refreshIslandLLMConfig(p)
	}
}

// refreshIslandLLMConfig re-materializes ONE island's provider key files, for
// the callers that already know which island changed.
func (s *Server) refreshIslandLLMConfig(p *project.Project) {
	if _, err := islandLLMConfigDir(p); err != nil {
		s.log.Warn("island llm config not refreshed", "island", p.Name, "err", err)
	}
}
