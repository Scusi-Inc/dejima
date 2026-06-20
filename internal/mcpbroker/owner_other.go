//go:build !unix

package mcpbroker

import "os"

// ownedByDaemon is a no-op off Unix (no stable uid model). The MCP broker runs
// on a Unix daemon host; this stub only keeps non-Unix client builds green.
func ownedByDaemon(_ os.FileInfo) error { return nil }
