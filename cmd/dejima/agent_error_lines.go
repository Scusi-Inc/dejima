package main

import "strings"

// agentErrorLinesMax bounds how much of an orchestration error the detail pane
// spends. Enough for a cause plus a remedy; not enough to push everything else
// off the screen.
const (
	agentErrorLinesMax = 6
	agentErrorWidth    = 58
)

// agentErrorLines splits an agent's orchestration error into pane-width lines,
// keeping its own line breaks and wrapping on spaces.
//
// It replaces truncate(err, 50). These errors are the one field in the pane that
// carries a REMEDY — a bundled agent whose binary cannot run reports the
// `dejima image build && dejima upgrade` that fixes it — and 50 characters
// removes the remedy while leaving the complaint, which is the worst half to
// keep. The façade setup steps failed the same way in the footer.
//
// Bounded rather than unbounded: a runaway error must not push the rest of the
// pane off screen, so the tail is marked with an ellipsis instead of dropped
// silently.
func agentErrorLines(err string) []string {
	var out []string
	for _, para := range strings.Split(strings.TrimSpace(err), "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		for _, ln := range wrapWords(para, agentErrorWidth) {
			if len(out) == agentErrorLinesMax {
				return append(out[:agentErrorLinesMax-1], out[agentErrorLinesMax-1]+" …")
			}
			out = append(out, ln)
		}
	}
	return out
}

// wrapWords breaks s on spaces at width, never mid-word unless a single word is
// itself longer than the line — a URL or a long path, which is worth keeping
// whole even when it overhangs, since a broken one cannot be copied.
func wrapWords(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		cur += " " + w
	}
	return append(lines, cur)
}
