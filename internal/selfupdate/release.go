package selfupdate

// release.go is the mutating release-mode path: download a published release
// asset, verify it against the release's SHA256SUMS (provenance bar = checksum
// over HTTPS — the same trust as the install.sh/irm one-liner, and strictly
// safer), then atomically replace a target binary. Used by `dejima update
// --apply` on a release install and by the daemon's self-update.
//
// Checksums catch corruption and a swapped asset; they do NOT defend against a
// compromised GitHub release (the attacker could regenerate SHA256SUMS too).
// Signed releases are the next hardening — see docs/self-update.md.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// releaseDownloadBase is the GitHub release-asset base URL, overridable in tests.
var releaseDownloadBase = "https://github.com/aoos/dejima/releases/download"

// AssetName returns the release archive name for a platform, matching the names
// produced by `make release-binaries` (see .github/workflows/release.yml):
// dejima_<ver>_<os>_<arch>.tar.gz, or .zip on Windows.
func AssetName(ver, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("dejima_%s_%s_%s.%s", ver, goos, goarch, ext)
}

// FetchBinary downloads the release asset for (ver, goos, goarch), verifies it
// against the release's SHA256SUMS, and extracts the binary named binName
// (e.g. "dejima", "dejima.exe", "dejimad") to destPath (0755).
func FetchBinary(ctx context.Context, ver, goos, goarch, binName, destPath string) error {
	asset := AssetName(ver, goos, goarch)
	base := releaseDownloadBase + "/" + ver
	archive, err := httpGet(ctx, base+"/"+asset)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	sums, err := httpGet(ctx, base+"/SHA256SUMS")
	if err != nil {
		return fmt.Errorf("download SHA256SUMS: %w", err)
	}
	if err := verifySHA256(archive, string(sums), asset); err != nil {
		return err
	}
	return extractBinary(archive, asset, binName, destPath)
}

// ApplyReleaseSelf updates the *running* executable to ver: it downloads the
// matching release, verifies it, and atomically replaces this binary in place.
// The install dir must be writable (the Windows client lives under LOCALAPPDATA;
// a root-owned /usr/local/bin needs elevation — caller surfaces that error).
func ApplyReleaseSelf(ctx context.Context, ver string, out io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil {
		self = resolved
	}
	dir := filepath.Dir(self)
	binName := filepath.Base(self) // "dejima" or "dejima.exe"

	// Stage beside the target when we can (same filesystem ⇒ atomic rename),
	// but fall back to a temp dir when the install dir isn't ours. A daemon
	// installed into a root-owned /usr/local/bin runs as the operator, so it
	// cannot even CREATE the staging file there — which surfaced as
	// "open /usr/local/bin/.dejimad.update: permission denied" and made
	// self-update impossible for the standard install layout.
	staged := filepath.Join(dir, "."+binName+".update")
	if !dirWritable(dir) {
		tmp, err := os.MkdirTemp("", "dejima-update-")
		if err != nil {
			return fmt.Errorf("stage update: %w", err)
		}
		defer os.RemoveAll(tmp)
		staged = filepath.Join(tmp, binName)
	}
	_ = os.Remove(staged)
	fmt.Fprintf(out, "downloading %s for %s/%s…\n", ver, runtime.GOOS, runtime.GOARCH)
	if err := FetchBinary(ctx, ver, runtime.GOOS, runtime.GOARCH, binName, staged); err != nil {
		return err
	}
	defer os.Remove(staged) // no-op once renamed into place
	if err := ReplaceExecutable(staged, self); err != nil {
		// A root-owned install dir is the normal layout, not an edge case, so
		// try the elevated path the service install already provisions for.
		if !isPermission(err) {
			return fmt.Errorf("replace %s: %w", self, err)
		}
		fmt.Fprintf(out, "install dir isn't writable; installing with sudo…\n")
		if serr := elevatedInstall(ctx, staged, self); serr != nil {
			return fmt.Errorf("replace %s: %w (and elevated install failed: %v)", self, err, serr)
		}
	}
	fmt.Fprintf(out, "updated %s → %s\n", binName, ver)
	return nil
}

// ReplaceExecutable atomically swaps target with the binary at newPath (same
// directory). On Windows a running .exe can't be overwritten, so the running
// one is renamed aside first (and rolled back on failure).
func ReplaceExecutable(newPath, target string) error {
	if err := os.Chmod(newPath, 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		// Windows can't unlink/overwrite a running image, but it CAN rename it
		// aside. Use a UNIQUE sidecar per attempt: a fixed ".old" breaks when a
		// prior same-session update already set the running TUI's image aside as
		// ".old" — that file is the live process image, so it's locked, os.Remove
		// can't clear it, and the rename collides with "Access is denied". A
		// timestamped name never collides. Sweep stale sidecars best-effort (the
		// still-locked running one stays and is harmless; cleaned a future run).
		//
		// The overriding safety goal on this path is: NEVER leave the install with
		// no dejima.exe. Every failure branch below either restores the original or
		// tells the user exactly how to reinstall.
		const reinstall = "reinstall the client with: irm https://dejima.tech/install-client.ps1 | iex"
		for _, f := range oldSidecars(target) {
			_ = os.Remove(f)
		}
		old := fmt.Sprintf("%s.old-%d", target, time.Now().UnixNano())
		if err := os.Rename(target, old); err != nil {
			// The original is untouched — nothing was moved — so this is safe to fail.
			return fmt.Errorf("set aside running exe: %w — %s", err, reinstall)
		}
		if err := os.Rename(newPath, target); err != nil {
			// Swap failed with the original set aside: put it back so the install
			// isn't left binary-less. If even the rollback fails, say so loudly.
			if rbErr := os.Rename(old, target); rbErr != nil {
				return fmt.Errorf("swap to new binary failed (%v) AND rollback failed (%v) — %s at %s may be missing; %s",
					err, rbErr, filepath.Base(target), target, reinstall)
			}
			return fmt.Errorf("swap to new binary failed, original restored: %w — if %s is missing, %s",
				err, filepath.Base(target), reinstall)
		}
		// The swap reported success; confirm the new binary is actually present
		// before we declare victory. If it somehow isn't, restore the original
		// rather than leaving a broken install.
		if _, statErr := os.Stat(target); statErr != nil {
			_ = os.Rename(old, target) // best-effort restore
			return fmt.Errorf("updated binary missing after swap (%v) — restored the original; if it's gone, %s",
				statErr, reinstall)
		}
		_ = os.Remove(old) // best-effort; locked while this build keeps running, swept next run
		return nil
	}
	// Unix: rename within the same directory is atomic and works even while the
	// old inode is still executing.
	return os.Rename(newPath, target)
}

// oldSidecars returns the set-aside "<target>.old*" files from prior Windows
// updates, so they can be swept best-effort (the one that's still a running
// image stays locked and is skipped harmlessly).
func oldSidecars(target string) []string {
	matches, _ := filepath.Glob(target + ".old*")
	return matches
}

// verifySHA256 confirms data's SHA-256 matches the entry for asset in a
// SHA256SUMS file ("<hex>  <name>" lines; a leading '*' binary-mode marker on
// the name is tolerated).
func verifySHA256(data []byte, sums, asset string) error {
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name != asset {
			continue
		}
		if strings.EqualFold(fields[0], want) {
			return nil
		}
		return fmt.Errorf("checksum mismatch for %s (got %s)", asset, want)
	}
	return fmt.Errorf("no checksum listed for %s", asset)
}

// extractBinary pulls the file named binName (matched by basename) out of the
// archive (tar.gz or zip, inferred from asset) and writes it to destPath (0755).
func extractBinary(archive []byte, asset, binName, destPath string) error {
	if strings.HasSuffix(asset, ".zip") {
		return extractZip(archive, binName, destPath)
	}
	return extractTarGz(archive, binName, destPath)
}

func extractTarGz(archive []byte, binName, destPath string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == binName {
			return writeFileFrom(destPath, tr)
		}
	}
	return fmt.Errorf("%s not found in archive", binName)
}

func extractZip(archive []byte, binName, destPath string) error {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != binName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		return writeFileFrom(destPath, rc)
	}
	return fmt.Errorf("%s not found in archive", binName)
}

// writeFileFrom writes r to path with 0755, truncating any prior staged file.
func writeFileFrom(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// dirWritable reports whether we can create files in dir.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".dejima-wtest-")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// isPermission reports whether err is (or wraps) a permission failure.
func isPermission(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

// ElevationAdvice is what to tell someone whose update could not elevate.
//
// It names the remedy that works for THEIR install, which the previous wording
// did not. `sudo -n` needs a NOPASSWD rule, and the only thing that writes one
// is `dejima service install --system` — so that was the only remedy offered.
// On a CLIENT-ONLY machine that advice is actively wrong: the client is in a
// root-owned /usr/local/bin, it has never run a service install, and it should
// not, because that installs a DAEMON SERVICE the operator does not want. It
// sent someone to set up a server in order to update a client. (Reported from
// a real client-only Mac, 2026-09-01.)
//
// `sudo dejima update` needs no rule and fixes both layouts: as root the
// install dir is writable, so the staged binary lands beside the target and the
// plain rename succeeds without reaching the elevation path at all.
//
// Exported and separate from the error so it can be asserted on without
// invoking sudo — a test that shells out here would pass for the wrong reason
// on any CI runner with passwordless sudo, which is most of them.
func ElevationAdvice() string {
	return "re-run as `sudo dejima update` (on a machine running the daemon, " +
		"`sudo dejima service install --system` grants passwordless updates instead)"
}

// elevatedInstall places src at target using sudo, for the normal layout where
// the install dir is root-owned but the daemon runs as the operator.
//
// The command is spelled to match the NOPASSWD rule `dejima service install
// --system` writes (/usr/bin/install -m 0755 <src> <pinned target>) — sudo
// matches arguments positionally, so the flags, their order, and the target
// must be exact or the grant does not apply. -n because the daemon has no TTY
// to answer a password prompt on: better a clear error than a silent hang.
func elevatedInstall(ctx context.Context, src, target string) error {
	if runtime.GOOS == "windows" {
		return errors.New("no elevation path on Windows")
	}
	cmd := exec.CommandContext(ctx, "sudo", "-n", "/usr/bin/install", "-m", "0755", src, target)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("sudo install %s: %s — %s", target, msg, ElevationAdvice())
	}
	return nil
}
