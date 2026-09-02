package wsl

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A DIAL IS WHAT BOOTS THE DISTRO. WSL shuts an idle distro down; the next
// `wsl.exe -d …` starts it, systemd then starts dejimad, and the socket appears
// a second or two later. Connecting immediately loses that race — the operator
// saw "Can't reach your Dejima host" moments after a setup that had just
// verified the connection.
//
// Run for real under /bin/sh against a socket that appears late, which is the
// cold-boot shape. Asserting on the string would only restate it.
func TestSocketExprWaitsForALateSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, ".dejima", "dejimad.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}

	// A stub socat that reports whether the socket existed when it ran.
	bin := t.TempDir()
	marker := filepath.Join(bin, "ran-with-socket")
	write(t, filepath.Join(bin, "socat"),
		"#!/bin/sh\n[ -S \""+sock+"\" ] && touch "+marker+"\nexit 0\n")

	// The socket shows up after the first sleep, as it does on a cold boot.
	go func() {
		time.Sleep(1200 * time.Millisecond)
		l, err := listenUnix(sock)
		if err == nil {
			defer l.Close()
			time.Sleep(6 * time.Second)
		}
	}()

	cmd := exec.Command("/bin/sh", "-c", socketExpr)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("socketExpr failed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Error("socat ran before the socket existed — a cold distro would report " +
			"\"Can't reach your Dejima host\" instead of waiting a moment for it")
	}
}

// A WARM socket must cost nothing. The wait exists for the cold case; paying it
// on every dial would add seconds to a dashboard that polls.
func TestSocketExprDoesNotWaitWhenTheSocketIsThere(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, ".dejima", "dejimad.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := listenUnix(sock)
	if err != nil {
		t.Skipf("cannot create a unix socket here: %v", err)
	}
	defer l.Close()

	bin := t.TempDir()
	write(t, filepath.Join(bin, "socat"), "#!/bin/sh\nexit 0\n")

	start := time.Now()
	cmd := exec.Command("/bin/sh", "-c", socketExpr)
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + dir}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("socketExpr failed with a warm socket: %v\n%s", err, out)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waited %v with the socket already present; the dashboard polls this path "+
			"and must not pay the cold-start delay every time", elapsed)
	}
}

// The distro's username is not knowable from Windows, so the path has to come
// from $HOME inside the distro. Pinned because hardcoding /root would work on
// the operator's distro and fail on anyone's who runs as a user.
func TestSocketExprResolvesHomeInsideTheDistro(t *testing.T) {
	if !strings.Contains(socketExpr, "$HOME") {
		t.Error("the socket path no longer resolves $HOME inside the distro; a hardcoded " +
			"path works only for distros whose user matches ours")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func listenUnix(path string) (interface{ Close() error }, error) {
	return net.Listen("unix", path)
}
