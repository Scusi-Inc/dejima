package selfupdate

import (
	"io"
	"os"
	"path/filepath"
)

// stagingDir is where a self-update parks the new binary so the privileged copy
// can name an EXACT source path.
//
// Under the operator's own ownership and 0700, not a world-writable /tmp: root
// is about to copy out of here, so anyone who can write this path can choose
// what becomes /usr/local/bin/dejima. 0700 keeps that to the one account the
// sudoers rule already trusts with exactly this operation.
const stagingDir = "/var/tmp/dejima-update"

// StagingPath is the fixed source path the privileged install reads for a given
// target, and the one the sudoers rule names.
//
// THE RULE AND THE CALLER MUST SPELL THE SAME STRING. sudo matches a command
// line positionally and literally, so a divergence here does not degrade — it
// makes the grant silently not apply, and a headless daemon's update dies with
// "a password is required" and no clue why. Both sides call this, so there is
// one spelling and no second place to update.
//
// Why a fixed path at all: the rule used to wildcard the source
// (`/usr/bin/install -m 0755 * /usr/local/bin/dejima`). sudo 1.9.17 rejects
// that outright —
//
//	syntax error: wildcards are not allowed in command arguments
//
// — because a wildcard in an argument is the classic way a narrow-looking grant
// turns into an arbitrary one. Pinning the source is both what the new parser
// requires and a strictly narrower grant than what it replaces.
func StagingPath(target string) string {
	return filepath.Join(stagingDir, filepath.Base(target))
}

// StagingDir exposes the directory so the installer can describe it.
func StagingDir() string { return stagingDir }

// ElevatedInstallArgv is THE command line, and the single place it is spelled.
//
// The sudoers rule authorises an exact argv and the updater runs an exact argv;
// they used to be written out separately in two packages, with a comment on each
// asking the next person to keep them in step. A mismatch has no symptom at the
// edit — it surfaces later as a headless daemon failing "a password is required"
// with a rule sitting right there that looks correct. Returning the argv from
// one function removes the question: the test that renders the rule builds its
// expectation from this, so the two cannot describe different commands.
func ElevatedInstallArgv(target string) []string {
	return []string{"/usr/bin/install", "-m", "0755", StagingPath(target), target}
}

// stageForElevatedInstall copies src to the pinned staging path with ownership
// and permissions that make it safe for root to read back.
//
// 0700 on the directory and 0600 on the file: between this copy and root's
// install, the file is a thing root will execute-install, so anyone able to
// replace it chooses the next /usr/local/bin/dejima. Narrowing to the invoking
// account keeps the window closed to everyone the sudoers rule does not already
// trust with precisely this.
func stageForElevatedInstall(src, staged string) error {
	if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
		return err
	}
	// Re-assert the mode: MkdirAll is a no-op on an existing directory, so a
	// pre-existing /var/tmp/dejima-update with looser bits would otherwise be
	// accepted as-is.
	if err := os.Chmod(filepath.Dir(staged), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
