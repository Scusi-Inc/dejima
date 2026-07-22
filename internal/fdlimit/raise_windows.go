package fdlimit

// Raise is a no-op on Windows, which has no RLIMIT_NOFILE — handle limits are
// governed by the process/desktop heap rather than a settable soft limit. The
// package still builds and links here because the client (which runs on
// Windows) pulls in the service templates, which reference Target.
func Raise() (Result, error) {
	return Result{Raised: false}, nil
}
