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
// WHAT IS SETTLED:
//
//   - NOT the daemon. Registering the provider is a store write plus a
//     re-materialize of every island's config files (registerLocalProvider ->
//     refreshIslandLLMConfigs); nothing about the daemon's own state is stale.
//   - THE AGENT, at minimum. The launch shim sources the .env at start, so a
//     process that was already running holds its start-time environment.
//
// THE SECOND BULLET IS STILL TRUE, AND IT HAS NOW BEEN WRONGLY DELETED ONCE.
//
// The first version of this note said "no island recreate", reasoning from
// llm_refresh.go that the provider config is a directory mount so a rewrite
// lands inside a running container. True — of islands that HAVE the mount. The
// /opt/host/llm bind was conditional at container create, so an island created
// before its operator had any provider — or with no agent that needed one,
// since claude-code and codex do not — had no mount at all.
//
// 8b850d1 (#396) made islandLLMConfigDir return the directory even when nothing
// resolves, so the bind is now unconditional. That is where the deletion came
// from, and it is a claim about the CREATE PATH ONLY. credentialBindMounts runs
// inside createContainerForProject and nowhere else — credential_mounts.go says
// so in its first sentence — so nothing can add a bind to a container that
// already exists. Every island created before that build still has no
// /opt/host/llm, and restarting its agent re-reads a path that is not in its
// filesystem.
//
// Nothing self-heals into it either: hibernateIsland only stops the container,
// and wakeIsland recreates ONLY when the status is Missing. A hibernate/wake
// cycle preserves the old mount set. Upgrade and reset are the only paths that
// recreate.
//
// So BOTH remedies stay, and the second is phrased to be self-diagnosing ("if
// that changes nothing") because the page cannot tell which island it is
// looking at. What CAN tell is credentialMountReport, which asks the runtime
// what is mounted and diffs it against config; /opt/host/llm is a row there as
// of #398, and configured=true mounted=false means exactly this case. Wiring
// that into this pane would let it name the island instead of offering a
// conditional — worth doing, not done here.
//
// The deletion was argued well and the argument was right about a case that had
// not stopped existing: "a hedge kept for a case that no longer exists is its
// own small lie". Correct, and the antecedent was false. A hedge deleted for a
// case that DOES still exist is the same lie with the sign flipped, and this
// one costs an evening: the operator restarts, nothing happens, the page has
// nothing further to say, and they conclude local models are broken.
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
	return "to use it from an agent — no daemon restart either way:\n" +
		"  · restart the agent so it re-reads its environment:  [s] on the agent → Restart\n" +
		"  · if that changes nothing, this island predates the always-present LLM config\n" +
		"    mount and has nothing to read. Recreate it:  [s] on the island → Upgrade"
}
