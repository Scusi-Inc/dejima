package sshfacade

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aoos/dejima/internal/project"
)

// clientSigner returns an ssh.Signer and its authorized_keys line.
func clientSigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer, string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
}

// startServer brings up the façade on a loopback listener with docker stubbed by
// `echo`, so the auth + channel plumbing is exercised without a real engine.
func startServer(t *testing.T) (*Server, net.Listener) {
	t.Helper()
	signer, err := HostSigner()
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{signer: signer, dockerBin: "echo", log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return srv, ln
}

func dial(t *testing.T, addr, user string, signer ssh.Signer) (*ssh.Client, error) {
	t.Helper()
	return ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
}

func TestSSHAuthAndExec(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := (&project.Project{Name: "isle"}).Save(); err != nil {
		t.Fatal(err)
	}
	if err := (&project.Project{Name: "other"}).Save(); err != nil {
		t.Fatal(err)
	}
	good, goodLine := clientSigner(t)
	if _, err := AddAuthorizedKey("isle", goodLine); err != nil {
		t.Fatal(err)
	}
	_, ln := startServer(t)
	addr := ln.Addr().String()

	t.Run("authorized key connects and exec relays output+exit", func(t *testing.T) {
		cl, err := dial(t, addr, "isle", good)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer cl.Close()
		sess, err := cl.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer sess.Close()
		out, err := sess.Output("whoami") // echo stub prints the docker argv, exit 0
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
		// `echo exec -i dejima-isle bash -c whoami` → output names the container.
		if !strings.Contains(string(out), "dejima-isle") || !strings.Contains(string(out), "whoami") {
			t.Fatalf("unexpected exec output: %q", out)
		}
		// exec must use a NON-login shell (`bash -c`), not `bash -lc`: a login
		// shell's profile output corrupts the channel VS Code parses for platform
		// detection. Guard against a regression to `-lc`.
		if strings.Contains(string(out), "-lc") {
			t.Fatalf("exec used a login shell (-lc); must be non-login `bash -c`: %q", out)
		}
		if !strings.Contains(string(out), "bash -c whoami") {
			t.Fatalf("expected non-login `bash -c whoami` in argv: %q", out)
		}
	})

	t.Run("sftp subsystem accepted, unknown rejected", func(t *testing.T) {
		cl, err := dial(t, addr, "isle", good)
		if err != nil {
			t.Fatal(err)
		}
		defer cl.Close()
		s1, err := cl.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := s1.RequestSubsystem("sftp"); err != nil {
			t.Fatalf("sftp subsystem rejected: %v", err)
		}
		_ = s1.Close()
		s2, err := cl.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		if err := s2.RequestSubsystem("bogus"); err == nil {
			t.Fatal("expected unknown subsystem to be rejected")
		}
		_ = s2.Close()
	})

	t.Run("unauthorized key is rejected", func(t *testing.T) {
		bad, _ := clientSigner(t)
		if cl, err := dial(t, addr, "isle", bad); err == nil {
			cl.Close()
			t.Fatal("expected handshake failure for unauthorized key")
		}
	})

	t.Run("authorized key cannot use it for another island", func(t *testing.T) {
		// good is authorized for "isle", not "other": connecting as user "other"
		// must fail — the username names the island and the key must match it.
		if cl, err := dial(t, addr, "other", good); err == nil {
			cl.Close()
			t.Fatal("expected handshake failure: key not authorized for 'other'")
		}
	})

	t.Run("invalid island name is rejected", func(t *testing.T) {
		if cl, err := dial(t, addr, "../etc", good); err == nil {
			cl.Close()
			t.Fatal("expected handshake failure for traversal username")
		}
	})
}
