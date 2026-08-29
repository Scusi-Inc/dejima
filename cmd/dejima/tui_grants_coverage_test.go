package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aoos/dejima/internal/api"
)

// "✓ fully contained" is the strongest sentence in the product, and it is
// derived by SUMMING an enumeration — grantKinds — rather than by inspecting the
// response. A grant kind the enumeration doesn't know about contributes zero, so
// the sum stays at zero, so the pane renders the reassuring claim in green over
// an island that holds the thing.
//
// A missing term is the dangerous shape here, not a wrong number. It fails
// toward a SMALLER, SAFER island than the one in front of the operator.
//
// TestGrantsSummariesCountEveryKind next door pins len(grantKinds) at 5, which
// catches someone editing grantKinds. It cannot catch the likelier order of
// events: a sixth kind added to api.IslandGrantsResponse and never wired in.
// Measured rather than assumed, on 2026-08-29 — a `Devices []string` field added
// to that struct and documented in openapi.yaml passes `go build ./...`, the
// whole cmd/dejima and internal/api suites, openapi_parity.py AND
// openapi_field_parity.py, while an island granted a device renders as fully
// contained. Nothing in the tree said a word.
//
// So this guard is keyed to the STRUCT, by reflection, and a new field is in
// scope the moment it exists.

// grantFieldPopulators teaches the guard how to make one field non-zero.
// Slices are handled generically; anything else needs an entry here.
//
// A field with no entry is a FAILURE, never a skip. Skipping is how this guard
// would go hollow: the next kind is exactly the field it wouldn't know how to
// populate, and "I couldn't test it" would print identically to "it's covered".
var grantFieldPopulators = map[string]func(reflect.Value){
	"HostGitHub": func(v reflect.Value) { v.FieldByName("Granted").SetBool(true) },
}

// grantFieldsThatAreNotGrants is the exemption list, and it is deliberately
// hostile to grow. Each entry needs a reason that survives being read aloud.
var grantFieldsThatAreNotGrants = map[string]string{
	// Credentials is what the RUNNING container has mounted, not something
	// granted. It reaches the containment claim by its own route (Known and
	// Drift() are separate conditions on the same branch) and is covered by
	// TestGrantsViewShowsCredentialDrift and
	// TestGrantsViewUnknownCredentialsSuppressesContainmentClaim.
	"Credentials": "a report on the running container, not a grant",
}

// containedBaseline is a response that legitimately reaches the containment
// claim: nothing granted, and the container inspected and in agreement.
//
// Known is set explicitly even though grantsModelWith would fill in an agreeing
// report for a zero value. Stated rather than inherited, because every render
// assertion below is a NEGATIVE — "fully contained" is absent — and a claim
// suppressed for an unrelated reason is indistinguishable from a claim correctly
// withheld. Relying on a helper's default to keep that path open is the kind of
// dependency that gets refactored away without anyone noticing the tests went
// vacuous.
//
// (Written first as "Known must be true or every case passes without asserting
// anything", which was false — the helper compensates. Checked, and corrected,
// rather than left as a plausible-sounding reason.)
func containedBaseline() *api.IslandGrantsResponse {
	return &api.IslandGrantsResponse{
		HostGitHub:  api.HostGitHubCredentialView{Eligible: true},
		Credentials: api.CredentialMountReport{Known: true},
	}
}

// grantedResponse returns a baseline response with exactly the named field
// granted, or ok=false if the guard doesn't know how to grant that field.
func grantedResponse(name string) (*api.IslandGrantsResponse, bool) {
	resp := containedBaseline()
	field := reflect.ValueOf(resp).Elem().FieldByName(name)
	if field.Kind() == reflect.Slice {
		field.Set(reflect.MakeSlice(field.Type(), 1, 1))
		return resp, true
	}
	pop, ok := grantFieldPopulators[name]
	if !ok {
		return nil, false
	}
	pop(field)
	return resp, true
}

// grantFields lists the fields of api.IslandGrantsResponse that claim to be
// grants, minus the exemptions.
func grantFields() []string {
	rt := reflect.TypeOf(api.IslandGrantsResponse{})
	var out []string
	for i := 0; i < rt.NumField(); i++ {
		if name := rt.Field(i).Name; grantFieldsThatAreNotGrants[name] == "" {
			out = append(out, name)
		}
	}
	return out
}

// grantCoverageGaps reports the fields the given enumeration fails to count, and
// separately the fields it could not even test. Taking the enumeration as a
// parameter is what lets the control below hand it a crippled one.
//
// unpopulatable is RETURNED rather than failed on the spot so that only the
// guard reports it. A helper that calls t.Errorf is fine until a control calls
// the helper too, at which point one defect prints three times and the extra
// copies read like extra defects.
func grantCoverageGaps(enumerate func(*api.IslandGrantsResponse) []grantKind) (gaps, unpopulatable []string) {
	for _, name := range grantFields() {
		resp, ok := grantedResponse(name)
		if !ok {
			unpopulatable = append(unpopulatable, name)
			continue
		}
		total := 0
		for _, k := range enumerate(resp) {
			total += k.n
		}
		if total == 0 {
			gaps = append(gaps, name)
		}
	}
	return gaps, unpopulatable
}

// The guard. Every grant-bearing field must move the total off zero, and must
// stop the pane claiming containment.
func TestEveryGrantFieldReachesTheContainmentClaim(t *testing.T) {
	fields := grantFields()
	if len(fields) == 0 {
		t.Fatal("found no grant fields on api.IslandGrantsResponse — this guard is no longer watching anything")
	}

	gaps, unpopulatable := grantCoverageGaps(grantKinds)
	for _, name := range unpopulatable {
		t.Errorf("api.IslandGrantsResponse.%s is a new field this guard cannot populate.\n"+
			"Add it to grantFieldPopulators (or to grantFieldsThatAreNotGrants with a reason), "+
			"then make sure grantKinds counts it — otherwise an island holding it renders "+
			"as \"fully contained\".", name)
	}
	for _, name := range gaps {
		t.Errorf("api.IslandGrantsResponse.%s is granted and grantKinds counts nothing, "+
			"so the pane calls the island fully contained. Add it to grantKinds.", name)
	}

	// The sum is the mechanism; the sentence is the product. Assert the sentence
	// too, so a future refactor that stops deriving the claim from the sum
	// doesn't leave this test passing about a quantity nobody renders.
	for _, name := range fields {
		resp, ok := grantedResponse(name)
		if !ok {
			continue // already reported above
		}
		if out := plain(grantsModelWith(resp).renderGrantsView()); strings.Contains(out, "fully contained") {
			t.Errorf("an island holding %s is not fully contained:\n%s", name, out)
		}
	}
}

// The control on the baseline. Every case above proves a NEGATIVE — the claim is
// absent — and a claim that is absent for an unrelated reason looks identical.
// If this fails, every assertion above is hollow and green.
func TestContainmentClaimIsReachableFromTheBaseline(t *testing.T) {
	out := plain(grantsModelWith(containedBaseline()).renderGrantsView())
	if !strings.Contains(out, "fully contained") {
		t.Fatalf("the baseline no longer reaches the containment claim, so every case in "+
			"TestEveryGrantFieldReachesTheContainmentClaim passes by absence:\n%s", out)
	}
}

// The control on the guard. A coverage check that cannot report a gap reports a
// clean sweep over anything — so hand it an enumeration with a known hole and
// require it to name that hole.
//
// The hole is HostGitHub deliberately: it is the kind that was actually dropped,
// twice, and it is the one field here that is not a slice, so it exercises the
// populator path rather than the generic one.
func TestGrantCoverageGuardSeesAnUncountedField(t *testing.T) {
	crippled := func(r *api.IslandGrantsResponse) []grantKind {
		var out []grantKind
		for _, k := range grantKinds(r) {
			if k.label == "GitHub" {
				continue // the omission this whole file exists to catch
			}
			out = append(out, k)
		}
		return out
	}
	gaps, _ := grantCoverageGaps(crippled)

	// Compare against the real enumeration rather than asserting gaps is exactly
	// [HostGitHub]. One thing changed, so one thing should appear — and a genuine
	// gap in grantKinds (which the guard above reports properly) must not also
	// make this control shout "the guard is decoration", which would be false and
	// would be the loudest line in the output.
	base, _ := grantCoverageGaps(grantKinds)
	real := map[string]bool{}
	for _, g := range base {
		real[g] = true
	}
	var caused []string
	for _, g := range gaps {
		if !real[g] {
			caused = append(caused, g)
		}
	}
	// If grantKinds ALREADY drops HostGitHub, crippling it changes nothing and
	// this control has no signal to offer. Say that, rather than the literally
	// available conclusion "the guard cannot see an uncounted kind" — which would
	// be false, and would accuse the guard in the one situation where the guard
	// is doing its job loudly one test above. A control that misattributes is
	// worse than one that abstains.
	if len(caused) == 0 && real["HostGitHub"] {
		t.Fatalf("cannot validate the guard: grantKinds already fails to count HostGitHub, "+
			"so removing it again changes nothing. Fix the failure reported by "+
			"TestEveryGrantFieldReachesTheContainmentClaim and this control becomes "+
			"meaningful again. (real gaps: %v)", base)
	}
	if len(caused) != 1 || caused[0] != "HostGitHub" {
		t.Fatalf("dropping the GitHub kind produced gaps %v (new: %v), want exactly "+
			"[HostGitHub] — the guard cannot see an uncounted grant kind, so the guard "+
			"above is decoration", gaps, caused)
	}
}

// The control on the exemption list. An exemption for a field that no longer
// exists is a silent widening: rename Credentials and the stale entry excuses
// nothing while the new field goes unexamined.
func TestGrantExemptionsAllNameRealFields(t *testing.T) {
	rt := reflect.TypeOf(api.IslandGrantsResponse{})
	for name, why := range grantFieldsThatAreNotGrants {
		if _, ok := rt.FieldByName(name); !ok {
			t.Errorf("grantFieldsThatAreNotGrants exempts %q (%s), which is not a field of "+
				"api.IslandGrantsResponse — the exemption is stale and excuses nothing", name, why)
		}
		if why == "" {
			t.Errorf("exemption %q has no reason; an unexplained exemption is how this list grows", name)
		}
	}
}
