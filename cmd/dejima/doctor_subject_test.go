package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// Three doctor rows shipped verdicts about things they could not see, and all
// three were observed contradicting reality on the same host on 2026-09-12:
//
//	supervision         in-island: "unsupervised (hand-run)"   host: launchd system LaunchDaemon
//	autonomy listener   host: "token-TCP off"                  five islands were using it
//	unattended restart  host: Docker Desktop menu path         the host runs colima
//
// Each is the same defect — a check whose predicate does not match its subject —
// so each gets a test that fixes the subject, plus a control proving the
// assertion can still fail. See docs/testing/guards-need-controls.md.

func fakeDaemonOverview(t *testing.T, o api.OverviewResponse) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "overview") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(o)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("DEJIMA_HOST", ts.URL)
}

// --- supervision ----------------------------------------------------------

// service.Detect() can only see the machine it runs on. Pointed at a daemon
// somewhere else — a laptop driving a Mac mini, or an agent inside an island
// dialling host.docker.internal — it inspects the wrong host's launchd and
// presents the answer as the daemon's.
//
// This doubles as its own control: the daemon here is reachable, which is the
// precondition for the old WARN branch. Delete the remote guard and this
// container produces either "unsupervised (hand-run)" or no row at all,
// and both assertions below fail.
func TestSupervisionDeclinesToJudgeADaemonItCannotSee(t *testing.T) {
	fakeDaemonOverview(t, api.OverviewResponse{})
	r := &doctorReport{}
	checkSupervision(context.Background(), r)

	status, text, found := findCheck(r, "supervision")
	if !found {
		t.Fatal("no supervision row: the check went silent instead of scoping itself to what it can see")
	}
	if status != "INFO" {
		t.Errorf("supervision status = %q, want INFO — the daemon runs on another host, so any verdict is about the wrong machine\n  %s", status, text)
	}
	if strings.Contains(text, "service install") {
		t.Errorf("supervision offered a fix for a host it cannot see:\n  %s", text)
	}
}

// --- autonomy listener ----------------------------------------------------

func TestAutonomyListenerVerdict(t *testing.T) {
	for _, tc := range []struct {
		name       string
		addr, kind string
		wantStatus string
		wantFix    bool
	}{
		// The regression: dejimad defaults the token listener on, so the operator
		// setting no flag is the ordinary WORKING case, not "off".
		{"daemon default", "127.0.0.1:7274", "loopback", "OK", false},
		{"operator set loopback", "127.0.0.1:9000", "explicit", "OK", false},
		// Non-loopback and correct. hostInternalBind relocates the default onto
		// the docker bridge gateway on a native engine, because a container there
		// cannot reach the host's loopback at all.
		{"relocated to bridge gateway", "172.17.0.1:7274", "bridge-gateway", "OK", false},
		// Non-loopback and wrong: a bearer-token listener on a LAN address.
		{"exposed to the LAN", "192.168.1.50:7274", "explicit", "WARN", true},
		{"listener never bound", "", "bind-failed", "WARN", true},
		// A daemon predating the field. Silence beats an unverified claim about a
		// security boundary — guessing was the bug.
		{"daemon too old to say", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, detail, fix := autonomyListenerVerdict(tc.addr, tc.kind)
			if status != tc.wantStatus {
				t.Errorf("status = %q, want %q (detail: %s)", status, tc.wantStatus, detail)
			}
			if (fix != "") != tc.wantFix {
				t.Errorf("fix present = %v, want %v (fix: %s)", fix != "", tc.wantFix, fix)
			}
			if status == "" && (detail != "" || fix != "") {
				t.Errorf("no status but non-empty row: detail=%q fix=%q", detail, fix)
			}
		})
	}
}

// Control for the bridge-gateway case above: prove that address alone really
// does point the other way, so the OK is the KIND's doing and not an accident
// of 172.17.0.1 happening to look loopback-ish to isLoopbackAddr. Without this,
// re-degrading the verdict to a bare address test would keep the table green.
func TestBridgeGatewayIsNotLoopbackAndStillPasses(t *testing.T) {
	const gw = "172.17.0.1:7274"
	if isLoopbackAddr(gw) {
		t.Fatalf("%s reads as loopback — this control can no longer tell the two rules apart", gw)
	}
	if status, _, _ := autonomyListenerVerdict(gw, "bridge-gateway"); status != "OK" {
		t.Errorf("status = %q, want OK: a relocated bind is host-internal, and warning on it "+
			"fires on every correctly configured native-Linux host", status)
	}
}

// The wiring: the row must come from what the daemon reports, not from a plist
// this process may not even have.
func TestListenerExposureReadsTheDaemon(t *testing.T) {
	fakeDaemonOverview(t, api.OverviewResponse{TokenAddr: "127.0.0.1:7274", TokenBindKind: "loopback"})
	r := &doctorReport{}
	checkListenerExposure(context.Background(), r)

	status, text, found := findCheck(r, "autonomy listener")
	if !found {
		t.Fatal("no autonomy listener row from a daemon that reported its bind")
	}
	if status != "OK" {
		t.Errorf("status = %q, want OK\n  %s", status, text)
	}
	if strings.Contains(text, "off") {
		t.Errorf("reported the listener as off while the daemon named its bind:\n  %s", text)
	}
}

// --- unattended restart ---------------------------------------------------

func TestColimaUnattendedVerdict(t *testing.T) {
	for _, tc := range []struct {
		name             string
		systemJob, agent bool
		autoLogin        tristate
		want             string
	}{
		{"system LaunchDaemon needs no login", true, false, triNo, "OK"},
		{"login agent plus auto-login", false, true, triYes, "OK"},
		{"login agent but a login screen", false, true, triNo, "WARN"},
		{"auto-login but nothing starts colima", false, false, triYes, "WARN"},
		{"nothing at all", false, false, triNo, "WARN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, detail, fix := colimaUnattendedVerdict(tc.systemJob, tc.agent, tc.autoLogin)
			if status != tc.want {
				t.Errorf("status = %q, want %q (%s)", status, tc.want, detail)
			}
			if (status == "WARN") != (fix != "") {
				t.Errorf("a WARN must carry a remedy and an OK must not: status=%q fix=%q", status, fix)
			}
		})
	}
}

// THE control for what actually shipped. Minion runs colima; doctor read Docker
// Desktop's settings store, found nothing, and printed a remedy naming
// "Docker Desktop → Settings → General" — a menu that does not exist on that
// machine. Advice for software the operator is not running is worse than none:
// it sends them hunting for a checkbox instead of at the real gap.
func TestUnattendedRemedyNeverNamesTheWrongEngine(t *testing.T) {
	_, _, colimaFix := colimaUnattendedVerdict(false, false, triNo)
	if colimaFix == "" {
		t.Fatal("no colima remedy to check — this control is now hollow")
	}
	for _, bad := range []string{"Docker Desktop", "Docker's setting"} {
		if strings.Contains(colimaFix, bad) {
			t.Errorf("colima remedy names %q:\n  %s", bad, colimaFix)
		}
	}
	if !strings.Contains(colimaFix, "colima") {
		t.Errorf("colima remedy never mentions colima:\n  %s", colimaFix)
	}

	_, _, desktopFix := unattendedHostVerdict(triNo, triNo)
	if desktopFix == "" {
		t.Fatal("no Docker Desktop remedy to check — this control is now hollow")
	}
	for _, bad := range []string{"colima", "brew services"} {
		if strings.Contains(desktopFix, bad) {
			t.Errorf("Docker Desktop remedy names %q:\n  %s", bad, desktopFix)
		}
	}
}
