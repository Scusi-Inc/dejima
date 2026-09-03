package api

import (
	"os"
	"path/filepath"
)

// islandUID is the uid the island image runs its agents as (image/Dockerfile:
// `useradd -m -s /bin/bash -u 1000 dejima`, then `USER dejima`). Anything the
// daemon materializes for a container to READ has to be reachable by this uid.
const islandUID = 1000

// makeIslandReadable makes a materialized credential reachable by the island's
// unprivileged user, without widening it to other users on the host.
//
// THE BUG THIS FIXES, from a real WSL host. The daemon materializes a
// single-identity gh config, mounts it read-only at /opt/host/gh-config, and
// writes it 0600 inside a 0700 directory. The daemon there runs as ROOT; the
// container runs as uid 1000. So the island could not read its own credential:
//
//	gh: failed to load config: open /opt/host/gh-config/config.yml: permission denied
//	ls: cannot open directory '/opt/host/gh-config': Permission denied
//
// and every clone of a private repo failed with "Authentication failed" — after
// `dejima github connect` had reported "islands can now clone and push as this
// identity". The credential existed, was bound to the island, was the default,
// and was mounted. It was simply unreadable.
//
// It works on Docker Desktop and colima because their VM file-sharing maps uids,
// so the container sees the file as its own. Remove the VM and the mapping goes
// with it — the SAME reason the egress proxy's loopback bind fails on a native
// engine. Two unrelated features, one hidden assumption: that a VM is in the
// middle.
//
// STRATEGY, in order:
//
//  1. chown to the island uid. Precise — only that uid gains access — and it is
//     what a root daemon (WSL, a system service) should do.
//  2. If chown fails (a daemon running as an ordinary user cannot chown to
//     another uid), widen the MODE instead. The containing ~/.dejima/secrets
//     tree stays 0700, so other users on the host still cannot traverse to it;
//     the container mounts the leaf directly and does not traverse the parent.
//
// Best-effort by design: on Docker Desktop neither step is needed and both may
// fail harmlessly. A hard error here would break the platform that works today
// in order to fix the one that does not.
// chownFn and chmodFn are seams. Without them this code is untestable on the
// machine that runs the tests: a runner that is ITSELF uid 1000 chowns to itself
// successfully, so the fix and a total no-op produce identical results. Three
// mutations — gutting the function, dropping +x on directories, widening the
// parent — all PASSED against the first version of the test for exactly that
// reason, which is the invalid-control shape documented in
// docs/testing/guards-need-controls.md.
var (
	chownFn = os.Chown
	chmodFn = os.Chmod
)

// islandReadablePerm is the mode to grant when ownership cannot be handed over.
// Directories need +x as well as +r or the island cannot ENTER them, which is
// the reported failure: `ls: cannot open directory '/opt/host/gh-config'`.
func islandReadablePerm(perm os.FileMode, isDir bool) os.FileMode {
	if isDir {
		return perm | 0o055
	}
	return perm | 0o044
}

func makeIslandReadable(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if err := chownFn(path, islandUID, -1); err == nil {
		return
	}
	_ = chmodFn(path, islandReadablePerm(info.Mode().Perm(), info.IsDir()))
}

// islandReadableTargets is the exact set of paths the tree walk will touch: the
// directory and its immediate entries, and NEVER the parent. The containing
// ~/.dejima/secrets tree must stay 0700 so other users on the host cannot
// traverse to a token; the container mounts the leaf directly and never walks up.
func islandReadableTargets(dir string) []string {
	out := []string{dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// makeIslandReadableTree applies makeIslandReadable to a directory and
// everything in it. Used for the materialized config DIRECTORIES, where the
// container has to enter the directory as well as read the files — the reported
// failure was `ls: cannot open directory`, i.e. the directory itself, before any
// file was reached.
func makeIslandReadableTree(dir string) {
	for _, p := range islandReadableTargets(dir) {
		makeIslandReadable(p)
	}
}
