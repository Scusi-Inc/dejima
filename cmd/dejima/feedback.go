package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/aoos/dejima/internal/ledger"
	"github.com/aoos/dejima/internal/version"
	"github.com/spf13/cobra"
)

// feedbackRepo is the public repo feedback issues are filed against.
const feedbackRepo = "aoos/dejima"

// feedbackLabel is applied to issues opened by `dejima feedback`.
const feedbackLabel = "feedback"

// ghCommandContext is an indirection over exec.CommandContext for `gh`, so the
// confirm→send path is testable without a real gh binary.
var ghCommandContext = func(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "gh", args...)
}

// ghLookPath is an indirection over exec.LookPath("gh") for tests.
var ghLookPath = func() (string, error) { return exec.LookPath("gh") }

// composedIssue is the exact, reviewable payload that becomes a GitHub issue.
// It carries ONLY the user's title/body plus an auto-generated environment
// footer of version + OS/arch — deliberately nothing else. No logs, paths,
// tokens, island names, env vars, or identity ever enter this struct.
type composedIssue struct {
	Title string
	Body  string // user message + environment footer
}

// environmentFooter renders the ONLY auto-collected context attached to a
// feedback issue: the CLI version and the OS/arch this binary was built for.
// This is intentionally the whole list — keep it that way. Adding anything that
// could identify a user, host, island, or workload (hostname, username, env,
// file paths, tokens, logs) would leak operator data into a PUBLIC issue.
func environmentFooter() string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("_Submitted via `dejima feedback`. Environment (the only auto-collected data):_\n\n")
	fmt.Fprintf(&b, "- dejima version: `%s`\n", version.Version)
	fmt.Fprintf(&b, "- os/arch: `%s/%s`\n", runtime.GOOS, runtime.GOARCH)
	return b.String()
}

// composeIssue builds the issue from a user message. The title is the first
// line (trimmed/clamped); the body is the full message followed by the
// environment footer.
func composeIssue(message string) composedIssue {
	message = strings.TrimSpace(message)
	title := firstNonEmptyLine(message)
	if title == "" {
		title = "Feedback"
	}
	title = clampTitle(title)

	var body strings.Builder
	if message != "" {
		body.WriteString(message)
		body.WriteString("\n\n")
	}
	body.WriteString(environmentFooter())
	return composedIssue{Title: title, Body: body.String()}
}

// firstNonEmptyLine returns the first non-blank line of s, trimmed.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// clampTitle keeps GitHub issue titles to a sane single-line length.
func clampTitle(s string) string {
	const max = 120
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return strings.TrimSpace(s[:max-1]) + "…"
}

// manualIssueURL builds a prefilled GitHub "new issue" URL so a user without a
// working `gh` can file the same issue by hand in a browser.
func manualIssueURL(iss composedIssue) string {
	q := url.Values{}
	q.Set("title", iss.Title)
	q.Set("body", iss.Body)
	q.Set("labels", feedbackLabel)
	return "https://github.com/" + feedbackRepo + "/issues/new?" + q.Encode()
}

// renderIssue prints the exact issue that would be filed (title + body) to w.
func renderIssue(w *strings.Builder, iss composedIssue) {
	w.WriteString("Repository: " + feedbackRepo + "\n")
	w.WriteString("Label:      " + feedbackLabel + "\n")
	w.WriteString("Title:      " + iss.Title + "\n")
	w.WriteString("\n")
	w.WriteString(iss.Body)
	if !strings.HasSuffix(iss.Body, "\n") {
		w.WriteString("\n")
	}
}

func newFeedbackCmd() *cobra.Command {
	var (
		yes    bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "feedback [message]",
		Short: "Open a public GitHub issue with feedback (carries version + OS only).",
		Long: "Files a public issue against " + feedbackRepo + " via `gh`. The issue carries " +
			"ONLY your message plus a footer with `dejima --version` and the OS/arch — never " +
			"logs, file paths, tokens, island names, environment variables, or identity.\n\n" +
			"The exact issue is shown before anything is sent; confirm to file it (or pass --yes " +
			"to skip the prompt, --dry-run to only print it). If `gh` is missing or unauthenticated, " +
			"the composed issue and a ready-to-paste manual URL are printed instead — it never blocks.\n\n" +
			"  dejima feedback \"port grant fails on a symlinked path\"\n" +
			"  dejima feedback            # prompts for the message\n" +
			"  dejima feedback --dry-run \"...\"",
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			message := strings.TrimSpace(strings.Join(args, " "))
			if message == "" {
				m, err := promptForMessage()
				if err != nil {
					return err
				}
				message = strings.TrimSpace(m)
			}
			if message == "" {
				return fmt.Errorf("no feedback message given; nothing to file")
			}

			iss := composeIssue(message)

			// Show-before-send: always print the exact payload first.
			var preview strings.Builder
			preview.WriteString("\nThe following PUBLIC issue will be filed (version + OS only — no logs, paths, or identity):\n\n")
			renderIssue(&preview, iss)
			fmt.Fprint(os.Stdout, preview.String())

			if dryRun {
				fmt.Fprintln(os.Stdout, "\n--dry-run: nothing was sent.")
				fmt.Fprintf(os.Stdout, "File it yourself:\n  %s\n", manualIssueURL(iss))
				recordFeedback("feedback.dry-run", "skipped", "")
				return nil
			}

			// gh missing/unauthed → graceful fallback, never block.
			if err := ghReady(cmd.Context()); err != nil {
				fmt.Fprintf(os.Stderr, "\n%s\n", ghUnavailableNote(err))
				fmt.Fprintf(os.Stdout, "File it yourself in a browser:\n  %s\n", manualIssueURL(iss))
				recordFeedback("feedback.manual", "denied", err.Error())
				return nil
			}

			if !yes {
				if !confirm("\nFile this issue now? [y/N]: ") {
					fmt.Fprintln(os.Stdout, "aborted; nothing was sent.")
					fmt.Fprintf(os.Stdout, "File it yourself anytime:\n  %s\n", manualIssueURL(iss))
					recordFeedback("feedback.abort", "denied", "user declined")
					return nil
				}
			}

			urlOut, err := createIssue(cmd.Context(), iss)
			if err != nil {
				// gh failed mid-flight — still leave the user a manual path.
				fmt.Fprintf(os.Stderr, "\ngh issue create failed: %v\n", err)
				fmt.Fprintf(os.Stdout, "File it yourself in a browser:\n  %s\n", manualIssueURL(iss))
				recordFeedback("feedback.error", "denied", err.Error())
				return nil
			}
			fmt.Fprintf(os.Stdout, "\nfiled: %s\n", urlOut)
			recordFeedback("feedback.submit", "allowed", urlOut)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt and file immediately")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compose and print the issue, but do not send it")
	return cmd
}

// promptForMessage reads a one-line feedback message from stdin when none was
// passed as an argument.
func promptForMessage() (string, error) {
	fmt.Fprint(os.Stdout, "Describe your feedback (one line), then Enter:\n> ")
	line, err := stdinReader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", fmt.Errorf("could not read feedback message: %w", err)
	}
	return line, nil
}

// confirm reads a y/N answer from the shared stdin reader. Default is No.
func confirm(prompt string) bool {
	fmt.Fprint(os.Stdout, prompt)
	line, _ := stdinReader.ReadString('\n')
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
}

// ghReady reports whether `gh` is installed and authenticated. A non-nil error
// is the reason to fall back to the manual URL.
func ghReady(ctx context.Context) error {
	if _, err := ghLookPath(); err != nil {
		return fmt.Errorf("gh CLI not found")
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if out, err := ghCommandContext(cctx, "auth", "status").CombinedOutput(); err != nil {
		return fmt.Errorf("gh not authenticated (%s)", feedbackFirstLine(string(out)))
	}
	return nil
}

// ghUnavailableNote turns a ghReady error into an actionable one-line note.
func ghUnavailableNote(err error) string {
	return fmt.Sprintf("Can't file automatically: %v. Install gh and run `gh auth login`, or use the link below.", err)
}

// createIssue files the composed issue via `gh issue create` and returns the
// resulting issue URL (gh prints it to stdout).
func createIssue(ctx context.Context, iss composedIssue) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := ghCommandContext(cctx,
		"issue", "create",
		"--repo", feedbackRepo,
		"--title", iss.Title,
		"--body", iss.Body,
		"--label", feedbackLabel,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s", feedbackFirstLine(string(out)))
	}
	return feedbackIssueURLFromOutput(string(out)), nil
}

// feedbackIssueURLFromOutput extracts the GitHub issue URL `gh issue create`
// prints. gh emits the URL as the last non-empty line; fall back to the whole
// trimmed output if no URL line is found.
func feedbackIssueURLFromOutput(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); strings.HasPrefix(t, "https://") {
			return t
		}
	}
	return strings.TrimSpace(out)
}

// recordFeedback appends an operator-act entry to the host-local Ledger, like
// other operator actions. Best-effort: a ledger failure must never block
// filing feedback. The entry records only the act and its decision — the issue
// URL on success — never the message body (which the user already reviewed and
// which lives in the public issue, not in operator records).
func recordFeedback(typ, decision, detail string) {
	lg, err := ledger.Default()
	if err != nil || lg == nil {
		return
	}
	_, _ = lg.Append(ledger.Entry{
		Type:     typ,
		Actor:    defaultOwner(),
		Decision: decision,
		Detail:   detail,
	})
}

// feedbackFirstLine returns the first non-empty line of s, trimmed.
func feedbackFirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
