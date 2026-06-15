package capability

import (
	"fmt"
	"runtime"

	"github.com/aoos/dejima/internal/paths"
)

// DefaultAdapter returns the capability adapter for this daemon host. Linux (and
// other Unix) get the script adapter, backed by ~/.dejima/capabilities/. macOS
// will get the Apple Shortcuts adapter in Phase 4; until then it errs rather than
// silently falling back to scripts (a Mac user expects Shortcuts, not a scripts
// dir). Selection is by host OS because the adapter is the daemon-side mapping;
// the wire contract (Request/Result) is identical across adapters.
func DefaultAdapter() (Adapter, error) {
	switch runtime.GOOS {
	case "darwin":
		return nil, fmt.Errorf("capability execution on macOS uses Apple Shortcuts, not yet implemented (capability-broker-spec.md phase 4)")
	default:
		dir, err := paths.CapabilitiesDir()
		if err != nil {
			return nil, err
		}
		return &ScriptAdapter{Dir: dir}, nil
	}
}
