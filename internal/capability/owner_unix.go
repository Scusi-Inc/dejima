//go:build unix

package capability

import (
	"fmt"
	"os"
	"syscall"
)

// ownedByDaemon requires the target be owned by the user the daemon runs as, so
// a file some other account dropped into the capabilities dir can't be invoked.
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
