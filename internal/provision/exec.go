package provision

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
)

// Runner abstracts command execution so phases can be unit-tested without a real
// host. Production uses execRunner; tests inject a fake.
type Runner interface {
	// Run executes name with args and returns combined stdout+stderr (trimmed)
	// and an error on non-zero exit or launch failure.
	Run(name string, args ...string) (string, error)
	// Look reports whether name resolves on PATH.
	Look(name string) bool
}

// execRunner is the real Runner backed by os/exec.
type execRunner struct{}

func (execRunner) Run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (execRunner) Look(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// confirm prompts y/N on c.Out, reading from c.In. Returns true only on an
// explicit yes. Under c.Yes it returns true without prompting — EXCEPT callers
// pass security-consequential prompts through confirmExplicit instead.
func (c *Context) confirm(prompt string) bool {
	if c.Yes {
		return true
	}
	return c.confirmExplicit(prompt)
}

// confirmExplicit always prompts, even under --yes. Use for steps with real
// blast radius (e.g. disabling SSH password auth) that --yes must not silently
// accept.
func (c *Context) confirmExplicit(prompt string) bool {
	fmt.Fprintf(c.Out, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(c.In).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// pauseForGUI prints a manual instruction and waits for the operator to press
// Enter (or records a skip under --yes so the run isn't blocked). Returns true
// if the operator continued, false if it was auto-skipped.
func (c *Context) pauseForGUI(instruction string) bool {
	fmt.Fprintln(c.Out, instruction)
	if c.Yes {
		fmt.Fprintln(c.Out, "  (--yes: skipping this manual step; it'll be on the final checklist)")
		return false
	}
	fmt.Fprint(c.Out, "  press Enter when done… ")
	_, _ = bufio.NewReader(c.In).ReadString('\n')
	return true
}

// say prints an informational line to the wizard output.
func (c *Context) say(format string, a ...any) {
	fmt.Fprintf(c.Out, format+"\n", a...)
}
