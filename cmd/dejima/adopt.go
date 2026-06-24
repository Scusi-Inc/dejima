package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoos/dejima/internal/api"
	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/reposrc"
)

// newAdoptCmd is the "adopt my existing project(s)" entry ramp. It wraps the
// existing primitives — local repo discovery + `reposrc.Resolve` (incl. the
// --local-copy seed that captures unpushed work) + the island-create path +
// the credential-bind APIs — into one guided flow: point it at projects you
// already have on disk and it stands up an island per project, seeded from your
// working copy, with GitHub/provider credentials bound.
//
// It is deliberately NOT new plumbing: every step delegates to code that
// `dejima init`, `dejima auth`, and `dejima provider` already use. The value is
// the orchestration — detection, the three project-shape cases, per-create
// confirmation, and idempotent/interruptible re-runs.
func newAdoptCmd() *cobra.Command {
	var (
		agent string
		yes   bool
		scan  string
	)
	cmd := &cobra.Command{
		Use:   "adopt [path ...]",
		Short: "Adopt existing local project(s) into Dejima islands (guided).",
		Long: "Turn projects you already have on disk into Dejima islands — one guided " +
			"command. For each project it creates an island seeded from your WORKING COPY " +
			"(via the --local-copy primitive, so unpushed commits and uncommitted work come " +
			"along), then binds the right credentials (your GitHub identity, LLM provider keys).\n\n" +
			"Pass project paths as arguments, or run it with none and it scans for git repos " +
			"under the current directory (override with --scan). It handles the common shapes:\n" +
			"  • a git repo with a remote        → seeded from your working copy; origin preserved\n" +
			"  • a git repo with unpushed work   → --local-copy captures the unpushed commits\n" +
			"  • a plain (non-git) directory     → offers to `git init` it first, then seeds it\n\n" +
			"It confirms before creating anything, and is safe to re-run: a project whose island " +
			"already exists is reported and skipped (interrupt and resume anytime).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdopt(cmd.Context(), adoptOpts{
				paths:     args,
				agent:     strings.TrimSpace(agent),
				assumeYes: yes,
				scanRoot:  strings.TrimSpace(scan),
			})
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "agent to run in each island: claude-code (default), codex, …")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the per-project confirmation prompt (still skips existing islands)")
	cmd.Flags().StringVar(&scan, "scan", "", "directory to scan for projects when no paths are given (default: current directory)")
	return cmd
}

type adoptOpts struct {
	paths     []string
	agent     string
	assumeYes bool
	scanRoot  string
}

// adoptShape classifies a candidate project by how it must be seeded.
type adoptShape int

const (
	shapeGitRemote   adoptShape = iota // git repo with an origin, no unpushed work
	shapeGitUnpushed                   // git repo with uncommitted/unpushed work
	shapeNonGit                        // a plain directory (no .git)
)

// adoptCandidate is one resolved project the wizard may turn into an island.
type adoptCandidate struct {
	path   string
	name   string // derived island name
	origin string // origin URL, or ""
	shape  adoptShape
	status reposrc.Status
}

func runAdopt(ctx context.Context, o adoptOpts) error {
	// `--local-copy` seeding bind-mounts a host path into the island, so it only
	// works when the daemon shares this filesystem. Fail early with the same
	// reasoning reposrc.Resolve uses, rather than silently falling back to
	// origin-only clones (which would drop unpushed work — the whole point here).
	daemonLocal := resolveHost() == ""

	fmt.Println()
	fmt.Println(bold("Adopt existing projects into Dejima"))
	fmt.Println()
	if !daemonLocal {
		fmt.Println("Note: DEJIMA_HOST points at a remote daemon, which can't read this machine's")
		fmt.Println("files. Adoption seeds islands from your local working copy, so it needs a LOCAL")
		fmt.Println("daemon. Projects with a remote can still be cloned from origin (unpushed work")
		fmt.Println("would be left behind); push first, or run this on the daemon host.")
		fmt.Println()
	}

	cands, err := collectCandidates(o, daemonLocal)
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		fmt.Println("Nothing to adopt. Pass project paths explicitly, e.g.:")
		fmt.Println("    dejima adopt ~/code/my-app ~/code/other-app")
		return nil
	}

	c, err := client()
	if err != nil {
		return err
	}

	// Idempotency: an island whose name matches an existing one is left alone, so
	// re-running after an interruption only creates what's missing.
	existing, err := existingIslandNames(ctx, c)
	if err != nil {
		return err
	}

	// Bind credentials once up front (they're account-wide on the daemon), then
	// reuse for every island created below.
	ensureCredentials(ctx, c)

	var created, skipped int
	for _, cand := range cands {
		if _, ok := existing[cand.name]; ok {
			fmt.Printf("• %s — island %q already exists; skipping (re-run safe).\n", cand.path, cand.name)
			skipped++
			continue
		}
		ok, err := adoptOne(ctx, c, cand, o, daemonLocal)
		if err != nil {
			// One project failing shouldn't abandon the rest of the batch.
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", cand.path, err)
			continue
		}
		if ok {
			created++
			existing[cand.name] = struct{}{}
		} else {
			skipped++
		}
	}

	fmt.Println()
	fmt.Printf("Done — %s created, %s skipped.\n", countNoun(created, "island"), countNoun(skipped, "project"))
	if created > 0 {
		fmt.Println("Next: `dejima ls` to see them, `dejima connect <name>` to jump in.")
	}
	return nil
}

// collectCandidates resolves the projects to consider: explicit path arguments
// when given, otherwise a scan of the chosen root for git repos.
func collectCandidates(o adoptOpts, daemonLocal bool) ([]adoptCandidate, error) {
	var paths []string
	if len(o.paths) > 0 {
		paths = o.paths
	} else {
		root := o.scanRoot
		if root == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			root = cwd
		}
		fmt.Printf("Scanning %s for projects…\n", root)
		repos, err := reposrc.Discover(root, 3)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", root, err)
		}
		for _, r := range repos {
			paths = append(paths, r.Path)
		}
		if len(paths) == 0 {
			fmt.Println("  (no git repos found under that directory)")
		} else {
			fmt.Printf("  found %s\n", countNoun(len(paths), "git repo"))
		}
		fmt.Println()
	}

	var cands []adoptCandidate
	for _, p := range paths {
		cand, err := classifyCandidate(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", p, err)
			continue
		}
		cands = append(cands, cand)
	}
	return cands, nil
}

// classifyCandidate inspects a path and determines its adopt shape. A git URL is
// rejected: adoption is for projects that live on THIS disk (use `dejima init
// --repo <url>` to clone a remote directly).
func classifyCandidate(in string) (adoptCandidate, error) {
	in = strings.TrimSpace(in)
	if reposrc.IsURL(in) {
		return adoptCandidate{}, fmt.Errorf("looks like a git URL — adoption is for local projects; use `dejima init --repo %s`", in)
	}
	abs, err := filepath.Abs(expandTilde(in))
	if err != nil {
		return adoptCandidate{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return adoptCandidate{}, fmt.Errorf("can't read %s: %w", in, err)
	}
	if !info.IsDir() {
		return adoptCandidate{}, fmt.Errorf("not a directory")
	}

	cand := adoptCandidate{
		path: abs,
		name: project.DeriveNameFromRepo(abs),
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		cand.shape = shapeNonGit
		return cand, nil
	}

	cand.origin, _ = reposrc.OriginURL(abs)
	cand.status = reposrc.GetStatus(abs)
	// "Unpushed work" = anything the canonical remote doesn't already have: a
	// dirty tree, commits ahead of upstream, or simply no remote at all. Those
	// are the cases where seeding from the working copy (rather than cloning
	// origin) actually captures something.
	if cand.origin == "" || cand.status.Dirty || cand.status.Ahead > 0 {
		cand.shape = shapeGitUnpushed
	} else {
		cand.shape = shapeGitRemote
	}
	return cand, nil
}

// adoptOne walks the confirmation + create for a single candidate. Returns
// (created, err): created=false with nil err means the user declined.
func adoptOne(ctx context.Context, c *api.Client, cand adoptCandidate, o adoptOpts, daemonLocal bool) (bool, error) {
	fmt.Println()
	fmt.Printf("%s  →  island %q\n", bold(cand.path), cand.name)
	fmt.Printf("  %s\n", describeShape(cand))

	// Non-git directories aren't seedable as-is (the island clones the seed's
	// .git). Offer to make it a repo first — that's the one transform adoption
	// performs on your disk, and only with consent.
	forceLocalCopy := true
	if cand.shape == shapeNonGit {
		if !daemonLocal {
			return false, fmt.Errorf("non-git project needs a local daemon to seed from the working copy")
		}
		if !adoptConfirm(o.assumeYes, fmt.Sprintf("  Run `git init` + an initial commit in %s so it can be adopted? [y/N]: ", cand.path)) {
			fmt.Println("  Skipped.")
			return false, nil
		}
		if err := gitInitWithCommit(ctx, cand.path); err != nil {
			return false, fmt.Errorf("git init: %w", err)
		}
		fmt.Println("  ✓ initialized a git repo (no remote — origin stays unset).")
	}

	// Reuse the exact resolution logic init/home use. forceLocalCopy keeps
	// unpushed work; for a clean git-with-remote repo it still seeds from the
	// working copy so adoption is consistent (the island is a faithful snapshot
	// of what's on disk, with origin preserved).
	res, err := reposrc.Resolve(cand.path, daemonLocal, forceLocalCopy)
	if err != nil {
		return false, err
	}
	fmt.Printf("  plan: %s\n", res.Note)

	if !adoptConfirm(o.assumeYes, fmt.Sprintf("  Create island %q now? [y/N]: ", cand.name)) {
		fmt.Println("  Skipped.")
		return false, nil
	}

	if res.Repo == "" && res.SeedPath == "" {
		return false, fmt.Errorf("nothing to seed (empty resolution)")
	}

	if err := ensureIslandImage(ctx, c); err != nil {
		return false, err
	}

	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	info, err := c.CreateIsland(cctx, api.CreateIslandRequest{
		Name:      cand.name,
		Repo:      res.Repo,
		SeedPath:  res.SeedPath,
		Agent:     o.agent,
		Owner:     defaultOwner(),
		Resources: api.Resources{},
	})
	if err != nil {
		return false, err
	}
	fmt.Printf("  ✓ created island %q (container: %s)\n", info.Name, info.Container)
	if info.Agent == api.AgentHeadless {
		fmt.Printf("    logs:    dejima logs %s --follow\n", info.Name)
	} else {
		fmt.Printf("    connect: dejima connect %s\n", info.Name)
	}
	return true, nil
}

func describeShape(cand adoptCandidate) string {
	switch cand.shape {
	case shapeNonGit:
		return "plain directory (not a git repo yet)"
	case shapeGitUnpushed:
		bits := []string{}
		if cand.origin == "" {
			bits = append(bits, "no remote")
		}
		if cand.status.Dirty {
			bits = append(bits, "uncommitted changes")
		}
		if cand.status.Ahead > 0 {
			bits = append(bits, fmt.Sprintf("%d unpushed commit(s)", cand.status.Ahead))
		}
		return "git repo with local work (" + strings.Join(bits, ", ") + ") — captured via local copy"
	default:
		return "git repo (origin: " + cand.origin + ")"
	}
}

// ensureCredentials checks what new islands will inherit and, when nothing is
// bound, offers to push the local GitHub identity. It only ever calls the
// existing credential-bind APIs; it never reimplements credential storage.
func ensureCredentials(ctx context.Context, c *api.Client) {
	fmt.Println(bold("Credentials"))

	// GitHub identity: islands clone/push as a daemon GitHub identity. If none is
	// bound and gh is logged in here, offer to seed it (same path as
	// `dejima auth push --github`).
	ids, err := c.ListGitHubIdentities(ctx)
	if err != nil {
		fmt.Printf("  (couldn't read GitHub identities: %v)\n", err)
	} else if len(ids) > 0 {
		fmt.Printf("  github: %s identit(ies) bound — new islands will use them.\n", okCount(len(ids)))
	} else if reposrc.GitHubAvailable() == nil {
		if adoptConfirm(false, "  github: none bound. Push this machine's gh login to the daemon now? [Y/n] (default yes): ") {
			if err := pushLocalGitHubIdentity(ctx, c); err != nil {
				fmt.Printf("    ✗ couldn't bind GitHub identity: %v\n", err)
			} else {
				fmt.Println("    ✓ bound your GitHub identity — islands can clone/push as you.")
			}
		}
	} else {
		fmt.Println("  github: none bound, and gh isn't logged in here. Islands fall back to the")
		fmt.Println("          host's ~/.config/gh. Bind one later with `dejima auth push --github`.")
	}

	// LLM provider keys: only key-requiring agents need them; report so the user
	// knows, but don't prompt for a secret in the adopt flow.
	provs, err := c.ListProviderCredentials(ctx)
	if err == nil {
		set := 0
		for _, p := range provs {
			if p.KeySet {
				set++
			}
		}
		if set > 0 {
			fmt.Printf("  llm:    %s provider key(s) set — key-requiring agents will use them.\n", okCount(set))
		} else {
			fmt.Println("  llm:    no provider keys set. claude-code uses pushed Claude creds; for")
			fmt.Println("          key-requiring agents add one with `dejima provider set <provider>`.")
		}
	}
	fmt.Println()
}

// pushLocalGitHubIdentity binds the active local gh account as a daemon GitHub
// identity, reusing the same reposrc + client APIs as `dejima auth push
// --github` (default host, made the default identity so islands pick it up).
func pushLocalGitHubIdentity(ctx context.Context, c *api.Client) error {
	login, token, err := reposrc.LocalGitHubLogin("")
	if err != nil {
		return err
	}
	_, err = c.PutGitHubIdentity(ctx, login, api.PutGitHubIdentityRequest{
		Login:   login,
		Token:   token,
		Default: true,
	})
	return err
}

// existingIslandNames returns the set of island names currently on the daemon,
// the basis for idempotent re-runs.
func existingIslandNames(ctx context.Context, c *api.Client) (map[string]struct{}, error) {
	islands, err := c.ListIslands(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(islands))
	for _, is := range islands {
		set[is.Name] = struct{}{}
	}
	return set, nil
}

// gitInitWithCommit turns a plain directory into a git repo with a single
// initial commit, so it can be seeded like any other repo. Best-effort author
// config is left to the user's global git config; we only -c override when no
// identity is set so the commit doesn't fail on a bare box.
func gitInitWithCommit(ctx context.Context, dir string) error {
	if err := runGit(ctx, dir, "init"); err != nil {
		return err
	}
	if err := runGit(ctx, dir, "add", "-A"); err != nil {
		return err
	}
	// If there's nothing to commit (empty dir), an initial empty commit still
	// gives the seed a valid .git to clone.
	args := []string{"commit", "-m", "Initial commit (adopted by Dejima)", "--allow-empty"}
	if !gitHasIdentity(ctx, dir) {
		args = append([]string{
			"-c", "user.name=Dejima",
			"-c", "user.email=dejima@localhost",
		}, args...)
	}
	return runGit(ctx, dir, args...)
}

func gitHasIdentity(ctx context.Context, dir string) bool {
	name := exec.CommandContext(ctx, "git", "-C", dir, "config", "user.name").Run() == nil
	email := exec.CommandContext(ctx, "git", "-C", dir, "config", "user.email").Run() == nil
	return name && email
}

func runGit(ctx context.Context, dir string, args ...string) error {
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// adoptConfirm prompts y/N. assumeYes short-circuits to true (used by --yes). An
// empty answer is treated as the prompt's stated default: prompts written
// "[Y/n]" default yes, everything else defaults no. (Separate from feedback.go's
// always-default-No confirm because the adopt flow needs both --yes and the
// per-prompt default.)
func adoptConfirm(assumeYes bool, prompt string) bool {
	if assumeYes {
		return true
	}
	ans := readSingleKey(prompt)
	if ans == "" {
		return strings.Contains(prompt, "[Y/n]")
	}
	return strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes")
}

// expandTilde resolves a leading ~ to the user's home directory (mirrors the
// unexported reposrc.expandHome, which isn't accessible here).
func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return p
}

func okCount(n int) string {
	return fmt.Sprintf("%d", n)
}
