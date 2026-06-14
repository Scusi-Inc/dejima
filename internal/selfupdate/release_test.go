package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAssetName(t *testing.T) {
	if got := AssetName("v0.1.11", "linux", "arm64"); got != "dejima_v0.1.11_linux_arm64.tar.gz" {
		t.Errorf("linux: %s", got)
	}
	if got := AssetName("v0.1.11", "windows", "amd64"); got != "dejima_v0.1.11_windows_amd64.zip" {
		t.Errorf("windows: %s", got)
	}
}

func TestVerifySHA256(t *testing.T) {
	data := []byte("hello dejima")
	sum := sha256.Sum256(data)
	hexsum := hex.EncodeToString(sum[:])
	sums := fmt.Sprintf("%s  dejima_v1_linux_amd64.tar.gz\n%s  other.zip\n", hexsum, "deadbeef")

	if err := verifySHA256(data, sums, "dejima_v1_linux_amd64.tar.gz"); err != nil {
		t.Errorf("valid checksum rejected: %v", err)
	}
	if err := verifySHA256([]byte("tampered"), sums, "dejima_v1_linux_amd64.tar.gz"); err == nil {
		t.Error("tampered data accepted")
	}
	if err := verifySHA256(data, sums, "not-listed.tar.gz"); err == nil {
		t.Error("unlisted asset accepted")
	}
	// Tolerate the binary-mode '*' filename marker.
	starred := fmt.Sprintf("%s *dejima_v1_linux_amd64.tar.gz\n", hexsum)
	if err := verifySHA256(data, starred, "dejima_v1_linux_amd64.tar.gz"); err != nil {
		t.Errorf("'*'-marked name rejected: %v", err)
	}
}

func makeTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "subdir/" + name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(content)
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func makeZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(content)
	zw.Close()
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	content := []byte("#!/bin/sh\necho dejima\n")

	t.Run("tar.gz", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "dejima")
		archive := makeTarGz(t, "dejima", content)
		if err := extractBinary(archive, "dejima_v1_linux_amd64.tar.gz", "dejima", dest); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(dest)
		if !bytes.Equal(got, content) {
			t.Errorf("extracted content mismatch: %q", got)
		}
		if info, _ := os.Stat(dest); info.Mode().Perm() != 0o755 {
			t.Errorf("perms = %v, want 0755", info.Mode().Perm())
		}
	})

	t.Run("zip", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "dejima.exe")
		archive := makeZip(t, "dejima.exe", content)
		if err := extractBinary(archive, "dejima_v1_windows_amd64.zip", "dejima.exe", dest); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(dest)
		if !bytes.Equal(got, content) {
			t.Errorf("extracted content mismatch: %q", got)
		}
	})

	t.Run("missing binary", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "dejima")
		archive := makeTarGz(t, "somethingelse", content)
		if err := extractBinary(archive, "x.tar.gz", "dejima", dest); err == nil {
			t.Error("expected error when binary absent from archive")
		}
	})
}

func TestReplaceExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix rename semantics")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "dejima")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, ".dejima.update")
	if err := os.WriteFile(staged, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceExecutable(staged, target); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "NEW" {
		t.Errorf("target = %q, want NEW", got)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o755 {
		t.Errorf("replaced binary perms = %v, want 0755", info.Mode().Perm())
	}
}

func TestFetchBinaryEndToEnd(t *testing.T) {
	content := []byte("dejima-binary-bytes")
	asset := AssetName("v9.9.9", runtime.GOOS, runtime.GOARCH)
	var archive []byte
	binName := "dejima"
	if runtime.GOOS == "windows" {
		binName = "dejima.exe"
		archive = makeZip(t, binName, content)
	} else {
		archive = makeTarGz(t, binName, content)
	}
	sum := sha256.Sum256(archive)
	sums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case filepath.Base(r.URL.Path) == asset:
			w.Write(archive)
		case filepath.Base(r.URL.Path) == "SHA256SUMS":
			w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	old := releaseDownloadBase
	releaseDownloadBase = srv.URL
	defer func() { releaseDownloadBase = old }()

	dest := filepath.Join(t.TempDir(), binName)
	if err := FetchBinary(context.Background(), "v9.9.9", runtime.GOOS, runtime.GOARCH, binName, dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, content) {
		t.Errorf("fetched binary mismatch: %q", got)
	}

	// A corrupted SHA256SUMS must make FetchBinary refuse.
	releaseDownloadBase = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "SHA256SUMS" {
			w.Write([]byte("0000  " + asset + "\n"))
			return
		}
		w.Write(archive)
	})).URL
	if err := FetchBinary(context.Background(), "v9.9.9", runtime.GOOS, runtime.GOARCH, binName, dest); err == nil {
		t.Error("expected checksum mismatch to fail the fetch")
	}
}
