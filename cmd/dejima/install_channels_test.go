package main

// Install-channel consistency gate (Lane A — the launch-gate lane).
//
// A virgin Mac can't be booted inside CI, so the human-run virgin-Mac proof
// (one section per channel, in the Lane A PR) is the end-to-end check. THIS test
// is the part that CAN run on every push: it parses the three install channels'
// source-of-truth files and asserts they agree on the things a broken install
// most often trips over —
//
//   · the release asset names the channels download all match the names
//     `make release-binaries` actually produces (`dejima_<vtag>_<os>_<arch>.<ext>`),
//   · the curl one-liner doesn't document a path that 404s (the file is
//     `install.sh` at the repo root — served at dejima.tech/install.sh — NOT
//     `scripts/install.sh`),
//   · the committed Homebrew formula is byte-identical to what its generator
//     emits for the formula's own version (so the "source of truth" claim holds),
//   · the install scripts are free of the partial-checkout brick (a re-clone over
//     an interrupted run) and have the fail-fast guards a Show-HN user needs.
//
// None of this needs Docker or a Mac, so it guards the install path on every PR.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readRepoFile reads a path relative to the repo root (the dir holding go.mod).
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// TestInstallChannels_AssetNamingMatchesReleaseBuild asserts every channel asks
// for the exact asset filename `make release-binaries` produces. A drift here is
// the classic "download failed: 404" on a clean machine.
func TestInstallChannels_AssetNamingMatchesReleaseBuild(t *testing.T) {
	mk := readRepoFile(t, "Makefile")

	// The Makefile is the producer. Confirm its archive names are the canonical
	// `dejima_<ver>_<os>_<arch>.{tar.gz,zip}` shape the consumers below assume —
	// if someone changes the producer, this anchors the rest of the test.
	wantTar := `dejima_$${ver}_$${os}_$${arch}.tar.gz`
	wantZip := `dejima_$${ver}_windows_$${arch}.zip`
	if !strings.Contains(mk, wantTar) {
		t.Errorf("Makefile release-binaries no longer produces %q — install channels assume this name", wantTar)
	}
	if !strings.Contains(mk, wantZip) {
		t.Errorf("Makefile release-binaries no longer produces %q — npm's windows path assumes this name", wantZip)
	}

	// install-client.sh — builds the unix tarball name from os/arch.
	client := readRepoFile(t, "install-client.sh")
	if !strings.Contains(client, `asset="dejima_${ver}_${os}_${arch}.tar.gz"`) {
		t.Error("install-client.sh asset name drifted from dejima_<ver>_<os>_<arch>.tar.gz")
	}

	// npm/install.js — builds both the tar.gz (unix) and zip (windows) names.
	npm := readRepoFile(t, "npm/install.js")
	if !strings.Contains(npm, "const asset = `dejima_${tag}_${plat}_${arch}.${ext}`;") {
		t.Error("npm/install.js asset name drifted from dejima_<tag>_<plat>_<arch>.<ext>")
	}
	if !strings.Contains(npm, "const tag = `v${version}`;") {
		t.Error("npm/install.js no longer prefixes the tag with 'v' — asset name would mismatch the release")
	}

	// The Homebrew formula's URLs must point at the same tarball name.
	formula := readRepoFile(t, "homebrew/dejima.rb")
	for _, suffix := range []string{
		"dejima_v#{version}_darwin_arm64.tar.gz",
		"dejima_v#{version}_darwin_amd64.tar.gz",
		"dejima_v#{version}_linux_arm64.tar.gz",
		"dejima_v#{version}_linux_amd64.tar.gz",
	} {
		if !strings.Contains(formula, suffix) {
			t.Errorf("homebrew/dejima.rb missing expected asset URL fragment %q", suffix)
		}
	}
}

// TestInstallScripts_CurlPathIsRepoRoot guards the bug where the self-documented
// one-liner pointed at scripts/install.sh (a 404 — the file lives at the repo
// root and is served at dejima.tech/install.sh).
func TestInstallScripts_CurlPathIsRepoRoot(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "install.sh")); err != nil {
		t.Fatalf("install.sh must live at the repo root (served at dejima.tech/install.sh): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "install.sh")); err == nil {
		t.Error("scripts/install.sh exists — the served path is the repo-root install.sh; a duplicate will drift")
	}

	// No doc string should advertise the dead scripts/install.sh download path.
	for _, rel := range []string{"install.sh", "README.md", "docs/distribution.md"} {
		body := readRepoFile(t, rel)
		if regexp.MustCompile(`githubusercontent\.com/aoos/dejima/[^ \n]*scripts/install\.sh`).MatchString(body) {
			t.Errorf("%s advertises a curl of scripts/install.sh (404) — the script is at the repo root", rel)
		}
	}
}

// TestHomebrewFormula_MatchesGenerator asserts the committed formula is exactly
// what scripts/gen-homebrew-formula.sh emits for the formula's own version +
// checksums. The generator is the documented single source of truth; if the two
// drift, `brew install --HEAD` review and the auto-bump diverge silently.
func TestHomebrewFormula_MatchesGenerator(t *testing.T) {
	formula := readRepoFile(t, "homebrew/dejima.rb")
	gen := readRepoFile(t, "scripts/gen-homebrew-formula.sh")

	// Pull the version the committed formula declares.
	verRe := regexp.MustCompile(`(?m)^\s*version "([^"]+)"`)
	m := verRe.FindStringSubmatch(formula)
	if m == nil {
		t.Fatal("could not find a version in homebrew/dejima.rb")
	}
	ver := m[1]

	// Reconstruct what the generator would print for this version + the formula's
	// own sha256 values, then compare to the committed file. We render the
	// generator's heredoc by substituting ${ver} and the four $(sha …) calls with
	// the values already in the committed formula — a self-consistency check that
	// catches a hand-edit to the formula that wasn't mirrored into the generator
	// (the file's own banner forbids hand-edits).
	shas := regexp.MustCompile(`sha256 "([0-9a-f]{64})"`).FindAllStringSubmatch(formula, -1)
	if len(shas) != 4 {
		t.Fatalf("expected 4 sha256 lines in the formula, found %d", len(shas))
	}

	// Extract the generator's heredoc body (between the first `cat <<EOF` and the
	// terminating `EOF`), then expand the shell substitutions it uses.
	body := gen
	start := strings.Index(body, "cat <<EOF\n")
	if start < 0 {
		t.Fatal("gen-homebrew-formula.sh has no `cat <<EOF` heredoc")
	}
	body = body[start+len("cat <<EOF\n"):]
	if end := strings.Index(body, "\nEOF\n"); end >= 0 {
		body = body[:end+1] // keep the trailing newline like the file does
	}
	// The generator escapes a few shell metachars in the heredoc (\`, \$, \\);
	// undo those to recover the literal text it would emit.
	repl := strings.NewReplacer(
		"${ver}", ver,
		"$(sha darwin_arm64.tar.gz)", shas[0][1],
		"$(sha darwin_amd64.tar.gz)", shas[1][1],
		"$(sha linux_arm64.tar.gz)", shas[2][1],
		"$(sha linux_amd64.tar.gz)", shas[3][1],
		"\\`", "`",
		"\\$", "$",
	)
	rendered := repl.Replace(body)

	if strings.TrimRight(rendered, "\n") != strings.TrimRight(formula, "\n") {
		t.Errorf("homebrew/dejima.rb is NOT what gen-homebrew-formula.sh would emit for version %s.\n"+
			"The generator is the source of truth (the formula banner forbids hand-edits).\n"+
			"Regenerate: scripts/gen-homebrew-formula.sh %s <SHA256SUMS> > homebrew/dejima.rb", ver, ver)
	}
}

// TestInstallSh_InterruptRetrySafe asserts install.sh recovers from a partial
// checkout (the interrupt/retry brick) rather than fataling on a re-run, and
// fetches tags so the baked version is a real release, not a bare hash.
func TestInstallSh_InterruptRetrySafe(t *testing.T) {
	sh := readRepoFile(t, "install.sh")

	// A re-clone path must exist for a non-git/partial SRC_DIR.
	if !strings.Contains(sh, "rm -rf \"$SRC_DIR\"") {
		t.Error("install.sh has no re-clone path for a partial/interrupted checkout — a retry will brick on `git clone` into a non-empty dir")
	}
	// rev-parse guards "looks like .git but isn't a real repo".
	if !strings.Contains(sh, "rev-parse --git-dir") {
		t.Error("install.sh doesn't validate the existing checkout (rev-parse) — a corrupt .git dir would fail update with no recovery")
	}
	// Tags must be fetched (the build bakes its version from `git describe --tags`).
	if !strings.Contains(sh, "fetch --quiet --tags") {
		t.Error("install.sh fetch omits --tags — `git describe --tags` will fall back to a bare hash as the version")
	}
	// A --depth 1 shallow clone of a branch has no tags → version baked as a hash.
	if strings.Contains(sh, "git clone --quiet --depth 1") {
		t.Error("install.sh shallow-clones (--depth 1) — that strips tags, so the build bakes a hash instead of a release version")
	}
	// Fail-fast prereq guards a Show-HN user needs (named cause + fix).
	for _, want := range []string{"command -v make", "command -v curl", "command -v git"} {
		if !strings.Contains(sh, want) {
			t.Errorf("install.sh missing a `%s` prereq guard — a missing tool would fail opaquely mid-build", want)
		}
	}
}

// TestInstallClientSh_VerifiesExtraction asserts the client installer fails with
// an actionable message (not a raw `install: no such file`) when the downloaded
// archive is corrupt or missing the binary.
func TestInstallClientSh_VerifiesExtraction(t *testing.T) {
	sh := readRepoFile(t, "install-client.sh")
	if !strings.Contains(sh, `[[ -f "$tmp/dejima" ]] || fail`) {
		t.Error("install-client.sh doesn't verify the extracted dejima binary exists — a corrupt download would fail with an opaque install error")
	}
	if !strings.Contains(sh, "checksum mismatch") {
		t.Error("install-client.sh dropped its checksum-mismatch guard")
	}
}
