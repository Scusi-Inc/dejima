package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aoos/dejima/internal/localmodel"
	"github.com/aoos/dejima/internal/providercreds"
	"github.com/aoos/dejima/internal/vmmem"
)

// Managed local models: the daemon orchestrates a host inference backend (Ollama
// by default) and auto-registers it as the `local` LLM provider so islands drive
// it as a thin OpenAI-compatible client. Islands reach the host endpoint via
// host.docker.internal, which is already in the egress NO_PROXY set — so no
// per-island grant is needed. See docs/local-models.md.

const (
	localInstallOKMarker = "--- dejima: local backend installed ---"
	localPullOKMarker    = "--- dejima: local model pulled ---"
)

// LocalModelsResponse is the `GET /v1/local/models` body: what's pulled on the
// host plus the host-aware recommendation of what to run.
type LocalModelsResponse struct {
	Pulled      []localmodel.InstalledModel `json:"pulled"`
	Recommended localmodel.Recommendation   `json:"recommended"`
}

// localBackend returns the configured host inference backend. Stateless (it just
// shells out to the host CLI), so a fresh value per call is fine.
func (s *Server) localBackend() localmodel.LocalBackend { return localmodel.NewOllama() }

// hostRAMGiB reports host RAM in whole GiB (0 when unknown), for model sizing.
func hostRAMGiB() int {
	b := vmmem.HostMemoryBytes()
	if b == 0 {
		return 0
	}
	return int(b / (1 << 30))
}

// localStatus assembles the full backend picture in one shot.
func (s *Server) localStatus(ctx context.Context) localmodel.Status {
	be := s.localBackend()
	installed, running := be.Detect(ctx)
	ram := hostRAMGiB()
	st := localmodel.Status{
		Backend:    be.Name(),
		Installed:  installed,
		Running:    running,
		Endpoint:   be.Endpoint(),
		HostRAMGiB: ram,
		Recommend:  localmodel.RecommendFor(ram),
		Provider:   localmodel.LocalProviderName,
	}
	if running {
		if models, err := be.List(ctx); err == nil {
			st.Models = models
		}
	}
	return st
}

// registerLocalProvider upserts the `local` providercreds entry pointed at the
// backend's endpoint. It sets EnvVar=OPENAI_API_KEY so the endpoint materializes
// as OPENAI_API_KEY + OPENAI_BASE_URL in an island — the shape OpenAI-compatible
// agents (aider, codex, goose, …) already read. The key is a dummy non-empty
// value: local backends ignore it, but some clients refuse to send an empty one.
func (s *Server) registerLocalProvider() error {
	be := s.localBackend()
	_, err := providercreds.Update(func(st *providercreds.Store) error {
		st.Put(providercreds.Provider{
			Name:    localmodel.LocalProviderName,
			EnvVar:  "OPENAI_API_KEY",
			APIKey:  "local",
			BaseURL: be.Endpoint(),
		})
		return nil
	})
	if err != nil {
		return err
	}
	// The endpoint can move between registrations (a backend reinstall, a
	// different port), so this is a rotation too, not only a first write.
	s.refreshIslandLLMConfigs()
	return nil
}

// startThenRegister brings the server UP and then registers the provider.
//
// An install that finishes with nothing listening is not a finished install. The
// operator got "install finished", a green tick, and a status line reading
// `installed (not running)` — with no command on any surface to start it. The
// backend was there and unusable, which is the same shape as a materialized key
// behind a missing mount: every signal says yes except the one that matters.
//
// A failed START does not fail the install and does not block registration. The
// install DID succeed, the provider config IS correct, and refusing to register
// would leave an operator with a working backend they cannot reach. It is
// reported in the stream instead, and `dejima local status` shows the truth.
func (s *Server) startThenRegister(w io.Writer) func() error {
	return func() error {
		be := s.localBackend()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := be.Start(ctx); err != nil {
			fmt.Fprintf(w, "the backend is installed but did not start: %v\n", err)
			fmt.Fprintln(w, "start it and re-run `dejima local install` to finish.")
		}
		return s.registerLocalProvider()
	}
}

func (s *Server) handleLocalStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.localStatus(r.Context()))
}

func (s *Server) handleLocalModels(w http.ResponseWriter, r *http.Request) {
	st := s.localStatus(r.Context())
	writeJSON(w, http.StatusOK, LocalModelsResponse{Pulled: st.Models, Recommended: st.Recommend})
}

// handleLocalInstall streams a best-effort backend install, then registers the
// `local` provider on success so models are immediately usable from islands.
func (s *Server) handleLocalInstall(w http.ResponseWriter, r *http.Request) {
	// Already there — installed by a previous run, or by hand because on macOS
	// the installer is the one part the daemon cannot drive (no terminal for its
	// sudo). Re-running it buys nothing; registration is the half that matters,
	// so this is the path back for anyone who installed the backend themselves.
	if installed, _ := s.localBackend().Detect(r.Context()); installed {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "%s is already installed on this host — registering the `local` provider.\n",
			s.localBackend().Name())
		if err := s.startThenRegister(w)(); err != nil {
			fmt.Fprintf(w, "provider registration failed: %v\n", err)
			return
		}
		fmt.Fprintln(w, localInstallOKMarker)
		return
	}
	stream, err := s.localBackend().Install(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	streamProgress(w, stream, localInstallOKMarker, s.startThenRegister(w))
}

// handleLocalPull streams `<backend> pull`. The {name} may be a curated alias or
// a raw backend ref; ResolveRef validates it. On success it (re)registers the
// provider so a freshly-pulled model's endpoint is wired up.
func (s *Server) handleLocalPull(w http.ResponseWriter, r *http.Request) {
	ref, _, err := localmodel.ResolveRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	stream, err := s.localBackend().Pull(r.Context(), ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer stream.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	streamProgress(w, stream, localPullOKMarker, s.registerLocalProvider)
}

func (s *Server) handleLocalRemove(w http.ResponseWriter, r *http.Request) {
	ref, _, err := localmodel.ResolveRef(r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.localBackend().Remove(r.Context(), ref); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"removed": ref})
}

// handleLocalOff deregisters the `local` provider (islands stop being offered it)
// without uninstalling the backend or deleting any pulled models.
func (s *Server) handleLocalOff(w http.ResponseWriter, r *http.Request) {
	if _, err := providercreds.Update(func(st *providercreds.Store) error {
		st.Remove(localmodel.LocalProviderName)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// "Islands stop being offered it" has to mean the key material is gone from
	// them, not just absent from the store's list.
	s.refreshIslandLLMConfigs()
	writeJSON(w, http.StatusOK, map[string]string{"status": "local provider disabled"})
}

// streamProgress copies a backend progress stream to the client with flushing,
// then runs onSuccess and writes okMarker — or an in-band "ERROR: …" line if the
// stream or onSuccess fails (status is already 200 by the time we stream). The
// CLI/SDK client parses the marker/ERROR sentinel, mirroring image build.
func streamProgress(w http.ResponseWriter, stream io.Reader, okMarker string, onSuccess func() error) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := stream.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // client hung up
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			if onSuccess != nil {
				if err := onSuccess(); err != nil {
					fmt.Fprintf(w, "\nERROR: %v\n", err)
					return
				}
			}
			fmt.Fprintf(w, "\n%s\n", okMarker)
			return
		}
		if rerr != nil {
			fmt.Fprintf(w, "\nERROR: %v\n", rerr)
			return
		}
	}
}
