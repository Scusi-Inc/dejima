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
	"sync"
	"syscall"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/sshfacade"
	"github.com/aoos/dejima/internal/version"
)

func main() {
	var (
		showVersion   bool
		debug         bool
		foreground    bool
		tcpAddr       string
		tokenAddr     string
		autonomyDial  string
		sshAddr       string
		hostTerminals bool
	)
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&debug, "debug", false, "enable debug logging")
	flag.BoolVar(&foreground, "foreground", false, "run in foreground (default; reserved for service-mode parity)")
	flag.StringVar(&tcpAddr, "tcp", os.Getenv("DEJIMAD_TCP"), "TCP listen addr (e.g. \":7273\"); empty disables. Accepts only Tailscale IPs.")
	flag.StringVar(&tokenAddr, "token-tcp", os.Getenv("DEJIMAD_TOKEN_TCP"), "host-internal TCP addr for the token-authenticated in-island autonomy path (e.g. \"127.0.0.1:7274\"); empty disables. Never bind a wildcard/LAN address.")
	flag.StringVar(&autonomyDial, "autonomy-dial", os.Getenv("DEJIMAD_AUTONOMY_DIAL"), "host:port an in-island brain dials to reach this daemon over --token-tcp (default \"host.docker.internal:<token-tcp port>\")")
	flag.StringVar(&sshAddr, "ssh", os.Getenv("DEJIMAD_SSH"), "SSH-façade listen addr (e.g. \"100.x.y.z:2222\" on the tailnet, or \":2222\"); empty disables. Auth is per-island public key; ssh <island>@<addr>.")
	flag.BoolVar(&hostTerminals, "host-terminals", os.Getenv("DEJIMAD_HOST_TERMINALS") == "1", "enable operator host terminals — UNCONTAINED shells on the daemon host (operator-only, never reachable by an island). Off by default.")
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

	if err := run(log, tcpAddr, tokenAddr, autonomyDial, sshAddr, hostTerminals); err != nil {
		log.Error("dejimad fatal", "err", err)
		os.Exit(1)
	}
}

// defaultTokenAddr is the loopback bind for the in-island token listener when
// the operator doesn't set --token-tcp. It's on by default because the control
// socket is no longer mounted into containers — this is the only in-island path.
const defaultTokenAddr = "127.0.0.1:7274"

func run(log *slog.Logger, tcpAddr, tokenAddr, autonomyDial, sshAddr string, hostTerminals bool) error {
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
	if hostTerminals {
		server.EnableHostTerminals()
		log.Warn("host terminals ENABLED — uncontained operator shells on this host are reachable to authenticated operators")
	}

	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Optional host-internal, token-authenticated listener: the in-island →
	// dejimad autonomy path (macOS, where the unix socket can't be mounted into
	// a container). Set up before serving so provisioning injects
	// DEJIMA_HOST/DEJIMA_TOKEN. The bind must be host-internal (loopback,
	// reachable via host.docker.internal) — never a wildcard/LAN address: the
	// token authorizes, the bind limits exposure.
	// On by default (loopback): an explicit --token-tcp that fails to bind is
	// fatal, but a failure of the default is best-effort — telemetry/autonomy
	// degrade to a no-op rather than bricking the daemon (e.g. on a port clash).
	tokenExplicit := tokenAddr != ""
	if tokenAddr == "" {
		tokenAddr = defaultTokenAddr
	}
	var tokenSrv *http.Server
	var tokenLn net.Listener
	if err := assertHostInternalBind(log, tokenAddr); err != nil {
		return err
	}
	dial := autonomyDial
	if dial == "" {
		_, port, splitErr := net.SplitHostPort(tokenAddr)
		if splitErr != nil {
			return fmt.Errorf("parse token-tcp addr %q: %w", tokenAddr, splitErr)
		}
		dial = "host.docker.internal:" + port
	}
	if tokenLn, err = net.Listen("tcp", tokenAddr); err != nil {
		if tokenExplicit {
			return fmt.Errorf("token-tcp listen %s: %w", tokenAddr, err)
		}
		log.Warn("default token listener bind failed; in-island telemetry/autonomy disabled (set --token-tcp to choose another address)", "addr", tokenAddr, "err", err)
	} else {
		defer tokenLn.Close()
		tokenSrv = &http.Server{
			Handler:           server.TokenAuthHandler(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		server.EnableAutonomy(dial)
		log.Info("autonomy enabled", "token_listener", tokenAddr, "container_dials", dial)
	}

	// Optional SSH-façade: the daemon is the single SSH endpoint for every island
	// (auth is per-island public key; the username names the island), bridging
	// into containers via `docker exec`. Unlike the token listener this is meant
	// to be reachable by external tools (VS Code/Cursor Remote-SSH, framework SSH
	// backends), so the operator picks the bind — prefer a tailnet address.
	var sshLn net.Listener
	var sshSrv *sshfacade.Server
	if sshAddr != "" {
		sshSrv, err = sshfacade.NewServer(log)
		if err != nil {
			return fmt.Errorf("ssh façade: %w", err)
		}
		sshLn, err = net.Listen("tcp", sshAddr)
		if err != nil {
			return fmt.Errorf("ssh listen %s: %w", sshAddr, err)
		}
		defer sshLn.Close()
		server.EnableSSH(sshAddr)
		log.Info("ssh façade enabled", "addr", sshAddr, "host_key", sshSrv.HostKeyFingerprint())
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Bring the container runtime up ourselves so the operator only has to start
	// dejimad. Best-effort: on failure we log and keep serving (doctor/status
	// still work) — the runtime can come up later.
	bootCtx, bootCancel := context.WithTimeout(ctx, 3*time.Minute)
	if err := rt.EnsureDaemon(bootCtx, log); err != nil {
		log.Warn("container runtime not ready; island operations will fail until it is", "err", err)
	}
	bootCancel()

	adoptCtx, adoptCancel := context.WithTimeout(ctx, 30*time.Second)
	server.AdoptExisting(adoptCtx)
	adoptCancel()

	errCh := make(chan error, 4)

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

	// Optional host-internal, token-authenticated listener (autonomy path).
	if tokenSrv != nil {
		go func() {
			log.Info("dejimad listening (token-tcp)", "addr", tokenLn.Addr().String())
			errCh <- tokenSrv.Serve(tokenLn)
		}()
	}

	// Optional SSH-façade listener.
	if sshSrv != nil {
		go func() {
			log.Info("dejimad listening (ssh)", "addr", sshLn.Addr().String())
			errCh <- sshSrv.Serve(sshLn)
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		if tokenSrv != nil {
			_ = tokenSrv.Shutdown(shutdownCtx)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// assertHostInternalBind refuses to bind the token listener to a wildcard/LAN
// address. The token is the authorization; the bind is the blast-radius limiter.
// A 0.0.0.0/:: bind would expose the autonomy API to the whole LAN, where only
// the bearer token stands between an attacker and the host. Loopback is ideal
// (reachable from containers via host.docker.internal on Docker Desktop); a
// non-loopback host-internal address (e.g. a docker bridge gateway) is allowed
// but warned, since the operator must ensure it isn't LAN-routable.
func assertHostInternalBind(log *slog.Logger, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("parse --token-tcp %q: %w", addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return fmt.Errorf("--token-tcp %q binds a wildcard address; the autonomy listener must bind a host-internal address (e.g. 127.0.0.1:<port>), never all interfaces", addr)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// A hostname rather than a literal IP — can't statically verify it; warn.
		log.Warn("token listener bind is not a literal IP; ensure it resolves to a host-internal address only", "addr", addr)
		return nil
	}
	if !ip.IsLoopback() {
		log.Warn("token listener bound to a non-loopback address; ensure it is not routable from the LAN", "addr", addr)
	}
	return nil
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
	log *slog.Logger

	mu          sync.Mutex
	tailnet     []netip.Prefix
	lastRefresh time.Time
}

// tailnetRefreshMinInterval rate-limits allowlist refreshes triggered by
// unknown addresses, so an off-tailnet scanner can't make us shell out to
// `tailscale status` on every probe.
const tailnetRefreshMinInterval = 10 * time.Second

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
		if !l.allowed(addr) {
			l.log.Warn("rejecting non-tailnet connection", "remote", host)
			_ = c.Close()
			continue
		}
		return c, nil
	}
}

// allowed reports whether addr is on the tailnet. On a miss it refreshes the
// allowlist (rate-limited) and re-checks: a peer that joined the tailnet after
// dejimad started isn't in the startup snapshot, and without this every new
// device would be rejected until the daemon restarts. Refreshing replaces the
// whole list, so removed peers also drop out on the next refresh.
func (l *tailscaleListener) allowed(addr netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if addrOnTailnet(addr, l.tailnet) {
		return true
	}
	if time.Since(l.lastRefresh) < tailnetRefreshMinInterval {
		return false
	}
	l.lastRefresh = time.Now()
	fresh, err := loadTailscaleIPs(l.log)
	if err != nil {
		l.log.Warn("tailnet allowlist refresh failed", "err", err)
		return false
	}
	l.tailnet = fresh
	l.log.Info("refreshed tailnet allowlist", "size", len(fresh))
	return addrOnTailnet(addr, l.tailnet)
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
		Self struct{ TailscaleIPs []string }            `json:"Self"`
		Peer map[string]struct{ TailscaleIPs []string } `json:"Peer"`
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
