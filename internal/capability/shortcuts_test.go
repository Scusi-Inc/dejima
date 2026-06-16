//go:build darwin

package capability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// On macOS, `shortcuts run` of a missing target must surface as ErrTargetNotFound
// (fail closed). This exercises the real CLI; it skips if the Shortcuts helper
// isn't reachable in the test context (e.g. a headless CI runner with no GUI
// session), since that's an environment limitation, not an adapter bug.
func TestShortcutsAdapter_NotFound(t *testing.T) {
	a := &ShortcutsAdapter{Timeout: 10 * time.Second}
	_, err := a.Execute(context.Background(), Request{
		Island: "oc", Target: "Dejima__definitely_no_such_shortcut__xyzzy",
	})
	if errors.Is(err, ErrTargetNotFound) {
		return // expected
	}
	if err != nil && strings.Contains(err.Error(), "helper") {
		t.Skip("Shortcuts helper unreachable in this context — needs a GUI session")
	}
	t.Fatalf("err = %v, want ErrTargetNotFound", err)
}
