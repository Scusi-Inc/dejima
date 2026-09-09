package api

import (
	"context"

	"github.com/aoos/dejima/internal/project"
	"github.com/aoos/dejima/internal/runtime"
)

// Is this island's container running the CURRENT island image?
//
// There was already a staleness signal, and it answered a different question
// than the one it was read as. islandSkewNote compares the island's version
// STAMP against the running daemon:
//
//	version.Compare(stamp, daemonVer) < 0  →  "stale image (X < Y)"
//
// The stamp is written at create and at upgrade, so it tracks the DAEMON's
// version, not the image's. Run `dejima image build` without a daemon version
// change — the ordinary way to pick up a Dockerfile fix — and every island still
// reports level while its container runs the old image. It also requires
// version.IsRelease on both sides, so a dev build is excluded entirely.
//
// That gap has a cost, and it was paid: an operator rebuilt the image, created a
// codex agent into an existing container, got a dead agent, and had nothing on
// any surface saying the island was behind. Deleting and re-adding the agent
// looked like the reasonable next thing to try, and could not have worked —
// agents share the island's one container, so the binary they reach for is
// whatever the image put there.
//
// A tag is a moving pointer. The honest comparison is between IMAGE IDENTITIES:
// the id `dejima/island:latest` resolves to now, against the id the container
// was actually created from. Those two disagree exactly when an upgrade is owed,
// under any daemon version, release or dev.

// ImageStale reports whether the container is running an image other than the
// one its tag points at now.
//
// THREE-STATE, and the third is the point: nil means "they match", a non-nil
// *true means "behind", and a nil-with-no-answer (the field left absent) means
// WE COULD NOT LOOK. An engine that will not answer knows nothing about
// staleness, and rendering that as "upgrade this island" would put a prompt on
// screen for an island that may be perfectly current — the same
// reassuring-direction failure as the reverse, and more annoying because it
// never clears.
func (s *Server) imageStale(ctx context.Context, p *project.Project) *bool {
	if st, _ := s.rt.Status(ctx, p.ContainerName()); st != runtime.StatusRunning {
		// A stopped or missing container is not "stale": there is nothing running
		// on the old image, and wake/create will use the current one anyway.
		return nil
	}
	image := p.Image
	if image == "" {
		image = DefaultImage
	}
	want, err := s.rt.ImageID(ctx, image)
	if err != nil || want == "" {
		s.log.Debug("image staleness: tag lookup failed", "island", p.Name, "err", err)
		return nil
	}
	got, err := s.rt.ContainerImageID(ctx, p.ContainerName())
	if err != nil || got == "" {
		s.log.Debug("image staleness: container lookup failed", "island", p.Name, "err", err)
		return nil
	}
	stale := got != want
	return &stale
}
