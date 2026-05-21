// Command dejimad is the Dejima host daemon. It manages island containers and
// exposes the Dejima API over a Unix socket (local clients) and optionally a
// Tailscale-pinned TCP listener (remote clients).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/version"
)

func main() {
	var (
		showVersion bool
		debug       bool
		foreground  bool
		tcpAddr     string
	)
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&debug, "debug", false, "enable debug logging")
	flag.BoolVar(&foreground, "foreground", false, "run in foreground (default; reserved for service-mode parity)")
	flag.StringVar(&tcpAddr, "tcp", os.Getenv("DEJIMAD_TCP"), "TCP listen addr (e.g. \":7273\"); empty disables. Accepts only Tailscale IPs.")
	flag.Parse()
	_ = foreground

	if showVersion {
		fmt.Println(version.Version)
		return
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(log, tcpAddr); err != nil {
		log.Error("dejimad fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, tcpAddr string) error {
	socketPath, err := paths.SocketPath()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(socketPath); statErr == nil {
		if probeErr := probeExistingDaemon(socketPath); probeErr == nil {
			return fmt.Errorf("another dejimad appears to be running on %s", socketPath)
		}
		log.Warn("removing stale socket", "path", socketPath)
		_ = os.Remove(socketPath)
	}

	unixLn, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	defer unixLn.Close()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}

	rt := runtime.NewDocker()
	em, err := events.New(log)
	if err != nil {
		return fmt.Errorf("events manager: %w", err)
	}
	server := api.NewServer(rt, log, em)

	handler := server.Handler()

	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	adoptCtx, adoptCancel := context.WithTimeout(ctx, 30*time.Second)
	server.AdoptExisting(adoptCtx)
	adoptCancel()

	errCh := make(chan error, 2)

	// Unix socket listener — always on, no auth (filesystem permissions).
	go func() {
		log.Info("dejimad listening (unix)", "socket", socketPath, "version", version.Version)
		errCh <- httpServer.Serve(unixLn)
	}()

	// Optional Tailscale-pinned TCP listener.
	var tcpLn net.Listener
	if tcpAddr != "" {
		tnet, err := loadTailscaleIPs(log)
		if err != nil {
			return fmt.Errorf("tailscale lookup: %w (install tailscale or run without --tcp)", err)
		}
		raw, err := net.Listen("tcp", tcpAddr)
		if err != nil {
			return fmt.Errorf("tcp listen %s: %w", tcpAddr, err)
		}
		tcpLn = &tailscaleListener{Listener: raw, tailnet: tnet, log: log}
		go func() {
			log.Info("dejimad listening (tcp)", "addr", tcpAddr, "tailnet_size", len(tnet))
			errCh <- httpServer.Serve(tcpLn)
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// probeExistingDaemon checks whether the socket has a healthy daemon behind it.
func probeExistingDaemon(socket string) error {
	conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// tailscaleListener wraps a net.Listener and refuses any connection whose
// remote address isn't on the local tailnet.
type tailscaleListener struct {
	net.Listener
	tailnet []netip.Prefix
	log     *slog.Logger
}

func (l *tailscaleListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		host, _, splitErr := net.SplitHostPort(c.RemoteAddr().String())
		if splitErr != nil {
			_ = c.Close()
			continue
		}
		addr, parseErr := netip.ParseAddr(host)
		if parseErr != nil {
			_ = c.Close()
			continue
		}
		if !addrOnTailnet(addr, l.tailnet) {
			l.log.Warn("rejecting non-tailnet connection", "remote", host)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

func addrOnTailnet(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// loadTailscaleIPs invokes `tailscale status --json` to enumerate peers and
// returns the union of their advertised tailnet addresses (as /32 or /128 prefixes).
// Also includes the host's own tailnet IPs so localhost-over-tailnet works.
func loadTailscaleIPs(log *slog.Logger) ([]netip.Prefix, error) {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("run tailscale status: %w", err)
	}
	var status struct {
		Self  struct{ TailscaleIPs []string } `json:"Self"`
		Peer  map[string]struct{ TailscaleIPs []string } `json:"Peer"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, fmt.Errorf("parse tailscale status: %w", err)
	}
	var prefixes []netip.Prefix
	add := func(ipStr string) {
		addr, parseErr := netip.ParseAddr(strings.TrimSpace(ipStr))
		if parseErr != nil {
			return
		}
		bits := 32
		if addr.Is6() {
			bits = 128
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, bits))
	}
	for _, ip := range status.Self.TailscaleIPs {
		add(ip)
	}
	for _, peer := range status.Peer {
		for _, ip := range peer.TailscaleIPs {
			add(ip)
		}
	}
	log.Debug("loaded tailnet allowlist", "size", len(prefixes))
	return prefixes, nil
}
