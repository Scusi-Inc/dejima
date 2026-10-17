package main

import (
	"github.com/aoos/dejima/internal/api"
)

// diskHeaderNotes renders the substrate-storage note for the header's server
// line, in two lengths: the full note and a short fallback for a narrow
// terminal. Both are "" when there is nothing worth saying.
//
// The free half is the half that changes a decision — what Dejima occupies is
// trivia next to whether the next island will fit — so the short form keeps
// free and drops the breakdown rather than the other way round.
//
// A zero DiskFreeBytes means UNDETERMINABLE, not full: free space is measured
// by `df` inside a running island, and a host with every island hibernated has
// nothing to measure through. That case says used-only and stays quiet about
// the rest, because "0 free" on a half-empty disk is the kind of wrong that
// gets acted on.
func diskHeaderNotes(o *api.OverviewResponse) (full, short string) {
	if o == nil {
		return "", ""
	}
	used, free := o.DiskUsedBytes, o.DiskFreeBytes
	switch {
	case used <= 0 && free <= 0:
		return "", ""
	case free <= 0:
		n := "disk " + humanBytes(uint64(used))
		return n, n
	case used <= 0:
		// Free space without a usage figure still answers the useful question.
		n := humanBytes(uint64(free)) + " free"
		return n, n
	}
	return "disk " + humanBytes(uint64(used)) + " · " + humanBytes(uint64(free)) + " free",
		humanBytes(uint64(free)) + " free"
}
