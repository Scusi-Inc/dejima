//go:build unix

package mcpbroker

import (
	"fmt"
	"os"
	"syscall"
)

// ownedByDaemon requires the MCP registry be owned by the user the daemon runs
// as, so a file some other account dropped in cannot name server programs the
// broker would exec.
func ownedByDaemon(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil // unknown stat backing; the mode checks already applied
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("not owned by the daemon user (owner uid %d, daemon uid %d)", st.Uid, os.Getuid())
	}
	return nil
}
