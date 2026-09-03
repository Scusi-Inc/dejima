package main

import (
	"fmt"
	"strings"

	"github.com/aoos/dejima/internal/localmodel"
)

// The local-models picker: choosing WHICH model to pull, from the curated
// catalog, on the screen rather than by retyping `dejima local pull <model>`.
//
// The page already offered two rows — install the backend, and pull the single
// recommended model. That is the right pair for a host the recommendation suits
// and no help at all otherwise: the catalog has six entries spanning 8 GiB to
// 256 GiB of host RAM, and an operator whose box sits between two of them, or
// who wants the small one for autocomplete rather than the big one, had to go
// and read `dejima local ls` to find out what the handles were.
//
// WHAT THE ROWS HAVE TO SAY, and it is more than the name:
//
//   - WHETHER IT FITS. MinRAMGiB is the total host RAM to run the model
//     comfortably, and a model that does not fit is not a choice — it is a swap
//     storm. It stays visible (knowing the 70B exists is useful) and is marked,
//     rather than being hidden, because a list that silently drops options
//     cannot be told apart from a list that is complete.
//   - WHETHER IT IS ALREADY PULLED, so "pull" is never offered for something
//     already on disk, and a multi-GB download is never started by accident.
//   - WHICH ONE THE HOST IS FOR. The recommendation is a fact about this
//     machine and it belongs beside the thing it recommends.
//
// HOST RAM CAN BE UNKNOWN (HostRAMGiB is 0 when the daemon could not determine
// it). Then nothing is marked as fitting or not fitting: an unknown is not a
// pass, and telling someone a model fits when we never measured is the
// reassuring-direction failure this codebase keeps producing.

// localModelRow is one catalog entry as the picker shows it.
type localModelRow struct {
	model  localmodel.Model
	pulled bool
	// fits is three-state on purpose: yes, no, and we-do-not-know. See above.
	fits      bool
	fitsKnown bool
	recommend bool
}

// localModelRows derives the picker's rows from the backend status.
func localModelRows(ls *localmodel.Status) []localModelRow {
	if ls == nil {
		return nil
	}
	rows := make([]localModelRow, 0, len(localmodel.Catalog))
	for _, mdl := range localmodel.Catalog {
		r := localModelRow{model: mdl, pulled: localModelPulled(ls, mdl)}
		if ls.HostRAMGiB > 0 {
			r.fitsKnown = true
			r.fits = ls.HostRAMGiB >= mdl.MinRAMGiB
		}
		if top := ls.Recommend.Top; top != nil && top.Alias == mdl.Alias {
			r.recommend = true
		}
		rows = append(rows, r)
	}
	return rows
}

// localModelRowLabel renders one row.
//
// The order is deliberate: mark, name, size, then the qualifiers. The name is
// what the operator is scanning for; the reason they should or should not pick
// it comes after, where it does not push the identity off a narrow terminal.
func localModelRowLabel(r localModelRow) string {
	mark := " "
	switch {
	case r.pulled:
		mark = "✓"
	case r.fitsKnown && !r.fits:
		mark = "!"
	}

	label := fmt.Sprintf("%s %-14s %-8s", mark, r.model.Alias, r.model.Params)

	var tags []string
	switch {
	case r.pulled:
		tags = append(tags, "pulled")
	case r.fitsKnown && !r.fits:
		// Say the number rather than "too big": the operator may be about to add
		// RAM, or may be looking at the wrong host entirely, and "needs 48 GiB" is
		// actionable where a verdict is not.
		tags = append(tags, fmt.Sprintf("needs %d GiB", r.model.MinRAMGiB))
	case !r.fitsKnown:
		// Never silently imply a fit we did not check.
		tags = append(tags, fmt.Sprintf("needs %d GiB · host RAM unknown", r.model.MinRAMGiB))
	}
	if r.recommend {
		tags = append(tags, "recommended for this host")
	}
	if len(tags) > 0 {
		label += "  " + strings.Join(tags, " · ")
	}
	return label
}

// localModelActions turns the picker rows into the runnable pull rows, in the
// order the page shows them.
//
// A PULLED MODEL GETS NO ROW. Re-pulling is not destructive, but it is a
// multi-gigabyte no-op, and an action that appears to do something while doing
// nothing is the shape this codebase has spent a week removing.
//
// A MODEL THAT DOES NOT FIT KEEPS ITS ROW. The operator may know something the
// RAM number does not — an external GPU, a machine about to be upgraded, a
// deliberate swap-tolerant experiment — and a picker that refuses is a picker
// people work around. The row says what it needs; the choice stays theirs.
func localModelActions(ls *localmodel.Status) []localAction {
	var acts []localAction
	seen := map[string]bool{}
	for _, r := range localModelRows(ls) {
		seen[r.model.Alias] = true
		if r.pulled {
			continue
		}
		acts = append(acts, localAction{
			label: "Pull " + localModelRowLabel(r),
			verb:  "pull " + r.model.Alias,
			args:  []string{"local", "pull", r.model.Alias},
		})
	}
	// A RECOMMENDATION FROM OUTSIDE THE CATALOG STILL GETS A ROW. Today
	// RecommendFor only ever returns a catalog entry, so this cannot fire — but
	// the recommendation is the backend's to compute, and a future one that names
	// something the catalog does not hold would otherwise lose its row silently
	// while every other row still rendered. Dropping the recommended action is
	// exactly the failure nobody would notice.
	if top := ls.Recommend.Top; top != nil && !seen[top.Alias] && !localModelPulled(ls, *top) {
		acts = append([]localAction{{
			label: fmt.Sprintf("Pull %s (%s) — recommended for this host", top.Alias, top.Params),
			verb:  "pull " + top.Alias,
			args:  []string{"local", "pull", top.Alias},
		}}, acts...)
	}
	return acts
}

// localModelsAppliedNote is what the page says after a model has been pulled.
//
// THE QUESTION IT ANSWERS is the one an operator asks next and the docs answer
// in a comment nobody reading this screen will find: what do I have to restart?
//
// The answer is narrower and cheaper than it looks, and it is worth stating
// exactly because every wider guess is a real cost:
//
//   - NOT the daemon. Registering the provider is a store write plus a
//     re-materialize of every island's config files (registerLocalProvider ->
//     refreshIslandLLMConfigs); nothing about the daemon's own state is stale.
//   - NOT the island, and not a recreate. The provider config is a DIRECTORY
//     mount, so a rewrite is visible inside a container that is already running.
//   - THE AGENT. The launch shim sources the .env at start, so a process that
//     was already running holds its start-time environment and cannot see a
//     provider that appeared afterwards.
//
// So the honest instruction is "restart the agents", and the TUI already has
// that exact affordance for exactly this reason — the secrets pane's [R] and the
// agent settings menu's Restart, both built for "you changed something the
// running process cannot see".
func localModelsAppliedNote(ls *localmodel.Status) string {
	if ls == nil || !ls.Installed {
		return ""
	}
	// AND NOT UNTIL A MODEL EXISTS. "islands can reach this now" was on screen
	// with the backend installed and nothing pulled, which is a capability claim
	// about an endpoint that would answer every request with "model not found".
	// Rendering it there is the same premature reassurance the rest of this file
	// is written against — caught by looking at the pane rather than by a test,
	// which is why the pane got looked at.
	if len(ls.Models) == 0 {
		return ""
	}
	return "islands can reach this now — but an agent that is ALREADY RUNNING kept its\n" +
		"start-time environment, so restart it to pick the provider up: [s] on the agent → Restart.\n" +
		"No island recreate and no daemon restart: the provider config is a directory mount."
}
