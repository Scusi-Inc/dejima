package localmodel

import "syscall"

// detachAttrs is a no-op on Windows: dejimad does not run there (see
// internal/service, which knows only launchd and systemd), so this file exists
// to keep the package building rather than to be used.
func detachAttrs() *syscall.SysProcAttr { return nil }
