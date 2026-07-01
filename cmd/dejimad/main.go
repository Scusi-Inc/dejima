// Command dejimad is the Dejima host daemon. It manages island containers and
// exposes the Dejima API over a Unix socket (local clients) and optionally a
// Tailscale-pinned TCP listener (remote clients).
package main

import (
	"bytes"
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
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/egress"
	"github.com/aoos/dejima/internal/events"
	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/paths"
	"github.com/aoos/dejima/internal/runtime"
	"github.com/aoos/dejima/internal/sshfacade"
	"github.com/aoos/dejima/internal/version"
)

// envDuration parses a Go duration (e.g. "4h") from env var key, returning 0 on
// an unset or malformed value — used as a flag default.
func envDuration(key string) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 0
}

func main() {
	var (
		showVersion   bool
		debug         bool
		foreground    bool
		tcpAddr       string
		tokenAddr     string
		autonomyDial  string
		egressAddr    string
		egressDial    string
		noEgress      bool
		sshAddr       string
		hostTerminals bool
		requireToken  bool

		audit            bool
		auditReads       bool
		auditHMACKeyFile string

		idleHibernate time.Duration
		wakeNotify    bool
		wakeFlush     time.Duration
	)
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&debug, "debug", false, "enable debug logging")
	flag.BoolVar(&foreground, "foreground", false, "run in foreground (default; reserved for service-mode parity)")
	flag.StringVar(&tcpAddr, "tcp", os.Getenv("DEJIMAD_TCP"), "TCP listen addr (e.g. \":7273\"); empty disables. Accepts only Tailscale IPs.")
	flag.StringVar(&tokenAddr, "token-tcp", os.Getenv("DEJIMAD_TOKEN_TCP"), "host-internal TCP addr for the token-authenticated in-island autonomy path (e.g. \"127.0.0.1:7274\"); empty disables. Never bind a wildcard/LAN address.")
	flag.StringVar(&autonomyDial, "autonomy-dial", os.Getenv("DEJIMAD_AUTONOMY_DIAL"), "host:port an in-island brain dials to reach this daemon over --token-tcp (default \"host.docker.internal:<token-tcp port>\")")
	flag.StringVar(&egressAddr, "egress-proxy", os.Getenv("DEJIMAD_EGRESS_PROXY"), "host-internal TCP addr for the island egress proxy (default \""+defaultEgressProxy+"\"). Islands route outbound HTTP(S) through it so destinations are observable and `dejima egress allow/deny` works with no daemon restart (Phase 1: observe-only, allow-all until a policy is set). On by default; disable with --no-egress-proxy.")
	flag.StringVar(&egressDial, "egress-dial", os.Getenv("DEJIMAD_EGRESS_DIAL"), "host:port islands dial to reach the egress proxy (default \"host.docker.internal:<egress-proxy port>\")")
	flag.BoolVar(&noEgress, "no-egress-proxy", os.Getenv("DEJIMAD_NO_EGRESS_PROXY") == "1", "disable the always-on island egress proxy. Outbound is then unobserved and `dejima egress allow/deny` is unavailable until re-enabled (which does require a restart). Off by default.")
	flag.StringVar(&sshAddr, "ssh", os.Getenv("DEJIMAD_SSH"), "SSH-façade listen addr (e.g. \"100.x.y.z:2222\" on the tailnet, or \":2222\"); empty disables. Auth is per-island public key; ssh <island>@<addr>.")
	flag.BoolVar(&hostTerminals, "host-terminals", os.Getenv("DEJIMAD_HOST_TERMINALS") != "0", "operator host terminals — UNCONTAINED shells on the daemon host (operator-only, never reachable by an island). On by default; disable with --host-terminals=false or DEJIMAD_HOST_TERMINALS=0.")
	flag.BoolVar(&requireToken, "require-token", os.Getenv("DEJIMAD_REQUIRE_TOKEN") == "1", "require an Authorization: Bearer <token> on the operator API (reject anonymous callers). Off by default — the unix socket and tailnet listener are otherwise trusted. Turn on when the daemon is reached by callers that should hold only an attenuated team-auth token.")
	flag.BoolVar(&audit, "audit", os.Getenv("DEJIMAD_AUDIT") == "1", "record an operational audit log (API requests + lifecycle events) to the hash-chained ~/.dejima/ledger.jsonl. Off by default; brokered Port/Trade/capability records are written regardless.")
	flag.BoolVar(&auditReads, "audit-reads", os.Getenv("DEJIMAD_AUDIT_READS") == "1", "with --audit, also record read (GET) requests; default records state-changing requests + lifecycle only.")
	flag.StringVar(&auditHMACKeyFile, "audit-hmac-key-file", os.Getenv("DEJIMAD_AUDIT_HMAC_KEY_FILE"), "path to a file holding an HMAC key; when set, the ledger chain is keyed (HMAC-SHA-256) so tamper-detection requires the key. Set on a FRESH ledger only.")
	flag.DurationVar(&idleHibernate, "idle-hibernate", envDuration("DEJIMAD_IDLE_HIBERNATE"), "hibernate a running island after this much idle time (no attached client, no live agent), e.g. \"4h\". Zero (default) disables it. Never touches an island with a live agent.")
	flag.BoolVar(&wakeNotify, "wake-notify", os.Getenv("DEJIMAD_WAKE_NOTIFY") != "0", "wake-on-message: nudge an idle agent (at its turn boundary) and wake a hibernated island when mail arrives. On by default; the mailbox.arrival event fires either way for wrapper-defined policy.")
	flag.DurationVar(&wakeFlush, "wake-flush-interval", envDuration("DEJIMAD_WAKE_FLUSH_INTERVAL"), "how often queued wake nudges are retried for delivery (a nudge that arrived while an agent was busy waits until its next turn boundary), e.g. \"30s\". Zero (default) uses the built-in 15s.")
	flag.Parse()
	_ = foreground

	// Egress proxy is ON by default so `dejima egress allow/deny` works without a
	// daemon restart (which would bounce every island). --no-egress-proxy (or
	// DEJIMAD_NO_EGRESS_PROXY=1) opts out; an explicit --egress-proxy <addr> picks
	// the bind. egressExplicit records operator intent: an explicitly-requested
	// proxy that fails to bind is fatal (the operator asked for it), but the
	// default-on proxy degrades gracefully (warn + run without it) so a busy port
	// can never block daemon startup.
	egressExplicit := os.Getenv("DEJIMAD_EGRESS_PROXY") != ""
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "egress-proxy" {
			egressExplicit = true
		}
	})
	switch {
	case noEgress:
		egressAddr = ""
	case egressAddr == "":
		egressAddr = defaultEgressProxy
	}

	if showVersion {
		fmt.Println(version.Version)
		return
	}

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	if err := run(log, tcpAddr, tokenAddr, autonomyDial, egressAddr, egressDial, egressExplicit, sshAddr, hostTerminals, requireToken,
		auditConfig{enabled: audit, reads: auditReads, hmacKeyFile: auditHMACKeyFile}, idleHibernate, wakeNotify, wakeFlush); err != nil {
		log.Error("dejimad fatal", "err", err)
		os.Exit(1)
	}
}

// auditConfig carries the operational-audit-log settings from flags into run().
type auditConfig struct {
	enabled     bool
	reads       bool
	hmacKeyFile string
}

// defaultTokenAddr is the loopback bind for the in-island token listener when
// the operator doesn't set --token-tcp. It's on by default because the control
// socket is no longer mounted into containers — this is the only in-island path.
const defaultTokenAddr = "127.0.0.1:7274"

// defaultEgressProxy is the host-internal bind for the island egress proxy when
// the operator doesn't set --egress-proxy. It's on by default so egress policy
// (allow/deny) can be applied without a daemon restart that bounces islands.
const defaultEgressProxy = "127.0.0.1:7280"

func run(log *slog.Logger, tcpAddr, tokenAddr, autonomyDial, egressAddr, egressDial string, egressExplicit bool, sshAddr string, hostTerminals, requireToken bool, audit auditConfig, idleHibernate time.Duration, wakeNotify bool, wakeFlush time.Duration) error {
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
	// Optional HMAC keying of the hash-chained ledger. Configure BEFORE the first
	// ledger append (NewServer/AdoptExisting can append), and on a fresh ledger
	// only — switching keying over a file with existing entries makes Verify
	// report the old entries as broken. Applies whether or not --audit is set,
	// since the brokered-operation records use the same chain.
	if audit.hmacKeyFile != "" {
		key, err := os.ReadFile(audit.hmacKeyFile)
		if err != nil {
			return fmt.Errorf("read audit HMAC key file %q: %w", audit.hmacKeyFile, err)
		}
		key = bytes.TrimSpace(key)
		if len(key) == 0 {
			return fmt.Errorf("audit HMAC key file %q is empty", audit.hmacKeyFile)
		}
		ledger.Configure(key)
		log.Info("ledger HMAC keying enabled", "key_file", audit.hmacKeyFile)
	}

	server := api.NewServer(rt, log, em)
	if hostTerminals {
		server.EnableHostTerminals()
		log.Warn("host terminals ENABLED — uncontained operator shells on this host are reachable to authenticated operators")
	}
	if requireToken {
		server.RequireToken()
		log.Info("operator API requires a bearer token — anonymous (no-token) requests will be rejected")
	}
	if audit.enabled {
		server.EnableAudit(api.AuditOptions{Reads: audit.reads})
		log.Info("operational audit log ENABLED", "record_reads", audit.reads)
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

	// Optional island egress proxy (Phase 1, observe-first): when enabled, every
	// new island routes outbound HTTP(S) through this proxy so destinations are
	// observable (nothing is blocked yet). Off unless --egress-proxy is set.
	// Mirrors the token-listener bind/dial split (host-internal bind; islands
	// dial host.docker.internal).
	var egressSrv *http.Server
	var egressLn net.Listener
	if egressAddr != "" {
		// A bind problem is fatal only when the operator explicitly asked for the
		// proxy; the default-on proxy degrades gracefully (warn + run without it)
		// so a busy port or a non-host-internal default can never block startup.
		// EnableEgress (which injects HTTP(S)_PROXY into new islands) is called
		// ONLY after the listener is up — so a failed bind injects no proxy env and
		// islands keep normal outbound, never routing into a dead proxy.
		fatal := func(err error) error {
			if egressExplicit {
				return err
			}
			log.Warn("island egress proxy off — `dejima egress allow/deny` unavailable until resolved (set --egress-proxy to a free host-internal addr, or --no-egress-proxy to silence)", "addr", egressAddr, "err", err)
			return nil
		}
		if err := assertHostInternalBind(log, egressAddr); err != nil {
			if ferr := fatal(err); ferr != nil {
				return ferr
			}
		} else if egressLn, err = net.Listen("tcp", egressAddr); err != nil {
			if ferr := fatal(fmt.Errorf("egress-proxy listen %s: %w", egressAddr, err)); ferr != nil {
				return ferr
			}
			egressLn = nil
		} else {
			defer egressLn.Close()
			eDial := egressDial
			if eDial == "" {
				_, port, splitErr := net.SplitHostPort(egressAddr)
				if splitErr != nil {
					return fmt.Errorf("parse egress-proxy addr %q: %w", egressAddr, splitErr)
				}
				eDial = "host.docker.internal:" + port
			}
			elog := egress.NewLog(256)
			var policyPath string
			if root, perr := paths.Root(); perr == nil {
				policyPath = filepath.Join(root, "egress-policy.json")
			}
			epolicy := egress.OpenPolicy(policyPath) // persisted per-island allow/deny; default observe (allow-all)
			egressSrv = &http.Server{
				Handler:           egress.NewProxy(elog, epolicy),
				ReadHeaderTimeout: 10 * time.Second,
			}
			server.EnableEgress(eDial, elog, epolicy)
			log.Info("island egress proxy enabled (default-on; observe-first; per-island policy)", "listener", egressAddr, "container_dials", eDial)
		}
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

	// Optional island egress proxy listener.
	if egressSrv != nil {
		go func() {
			log.Info("dejimad listening (egress-proxy)", "addr", egressLn.Addr().String())
			errCh <- egressSrv.Serve(egressLn)
		}()
	}

	// Announce the daemon is up (push signal for headless hosts) and start the
	// container watchdog that emits container.crashed on unexpected exits.
	listenModes := []string{"unix"}
	if tcpAddr != "" {
		listenModes = append(listenModes, "tcp")
	}
	if tokenSrv != nil {
		listenModes = append(listenModes, "token-tcp")
	}
	if sshSrv != nil {
		listenModes = append(listenModes, "ssh")
	}
	if egressSrv != nil {
		listenModes = append(listenModes, "egress-proxy")
	}
	server.EmitDaemonStarted(listenModes)
	go server.RunWatchdog(ctx, 0)
	go server.RunIdleHibernator(ctx, idleHibernate) // no-op when idleHibernate == 0
	go server.RunHeartbeatMonitor(ctx)              // operator alert when a running agent goes silent
	go server.RunScheduler(ctx)                     // fire durable per-island scheduled wakes
	go server.RunSpawnReaper(ctx)                   // reap exited/aged/orphaned ephemeral sub-agents
	server.SetWakeNotify(wakeNotify)
	go server.RunWakeNotifier(ctx, wakeFlush) // wake-on-message (Lane 5 P3.5); no-op when disabled

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		if tokenSrv != nil {
			_ = tokenSrv.Shutdown(shutdownCtx)
		}
		if egressSrv != nil {
			_ = egressSrv.Shutdown(shutdownCtx)
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
