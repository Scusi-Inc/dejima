package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Docker shells out to the `docker` CLI. It is intentionally simple: no
// SDK dependency, easy to debug, easy to swap with `podman` by overriding Bin.
type Docker struct {
	// Bin is the docker binary. Defaults to "docker".
	Bin string
}

// NewDocker returns a Docker runtime backed by the `docker` CLI.
func NewDocker() *Docker { return &Docker{Bin: "docker"} }

func (d *Docker) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

func (d *Docker) run(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (d *Docker) runOK(ctx context.Context, args ...string) (string, error) {
	out, errOut, err := d.run(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut))
	}
	return out, nil
}

func (d *Docker) EnsureVolume(ctx context.Context, name string) error {
	// `docker volume create` is idempotent.
	_, err := d.runOK(ctx, "volume", "create", name)
	return err
}

// CopyVolumeData copies the contents of src into dst via a throwaway container
// that mounts both volumes (src read-only) and runs `cp -a`. image must provide
// a POSIX sh + cp (the island image does). Used by island clone.
func (d *Docker) CopyVolumeData(ctx context.Context, src, dst, image string) error {
	_, err := d.runOK(ctx, "run", "--rm",
		"-v", src+":/from:ro",
		"-v", dst+":/to",
		image, "sh", "-c", "cp -a /from/. /to/")
	return err
}

func (d *Docker) RemoveVolume(ctx context.Context, name string, force bool) error {
	args := []string{"volume", "rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, name)
	_, err := d.runOK(ctx, args...)
	return err
}

func (d *Docker) EnsureNetwork(ctx context.Context, name string) error {
	// `docker network create` errors if the network exists; check first.
	if _, _, err := d.run(ctx, "network", "inspect", name); err == nil {
		return nil
	}
	_, err := d.runOK(ctx, "network", "create", "--driver", "bridge", name)
	return err
}

func (d *Docker) RemoveNetwork(ctx context.Context, name string) error {
	_, _, err := d.run(ctx, "network", "rm", name)
	return err // tolerate missing
}

func (d *Docker) Stats(ctx context.Context, name string) (Stats, error) {
	// `docker stats --no-stream --format` gives one-shot stats.
	out, _, err := d.run(ctx, "stats", "--no-stream", "--format",
		"{{.MemUsage}}|{{.MemPerc}}|{{.CPUPerc}}", name)
	if err != nil {
		return Stats{}, nil // best-effort; not running, etc.
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 3 {
		return Stats{}, nil
	}
	// MemUsage looks like "123MiB / 4GiB".
	used, limit := parseMemUsage(parts[0])
	cpu := parsePercent(parts[2])
	return Stats{
		MemoryUsageBytes: used,
		MemoryLimitBytes: limit,
		CPUPercent:       cpu,
	}, nil
}

func (d *Docker) StatsAll(ctx context.Context) (map[string]Stats, error) {
	out, _, err := d.run(ctx, "stats", "--no-stream", "--format",
		"{{.Name}}|{{.MemUsage}}|{{.MemPerc}}|{{.CPUPerc}}")
	if err != nil {
		return nil, err
	}
	all := map[string]Stats{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 4 {
			continue
		}
		used, limit := parseMemUsage(parts[1])
		all[parts[0]] = Stats{
			MemoryUsageBytes: used,
			MemoryLimitBytes: limit,
			CPUPercent:       parsePercent(parts[3]),
		}
	}
	return all, nil
}

// VolumeSizes returns each volume's on-disk size in bytes via a single
// `docker system df -v` query. Best-effort: returns nil on error, and 0 for any
// volume whose size the storage driver doesn't report (shows as "N/A").
func (d *Docker) VolumeSizes(ctx context.Context) (map[string]int64, error) {
	out, _, err := d.run(ctx, "system", "df", "-v", "--format", "{{json .Volumes}}")
	if err != nil {
		return nil, err
	}
	var vols []struct {
		Name string `json:"Name"`
		Size string `json:"Size"` // human string, e.g. "1.2GB" or "N/A"
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &vols); err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(vols))
	for _, v := range vols {
		sizes[v.Name] = int64(parseBytes(strings.TrimSpace(v.Size)))
	}
	return sizes, nil
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "%")
	v, _ := parseFloat(s)
	return v
}

func parseMemUsage(s string) (used, limit uint64) {
	// "123MiB / 4GiB"
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0
	}
	return parseBytes(strings.TrimSpace(parts[0])), parseBytes(strings.TrimSpace(parts[1]))
}

func parseBytes(s string) uint64 {
	multipliers := []struct {
		suffix string
		mult   uint64
	}{
		{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10},
		{"GB", 1_000_000_000}, {"MB", 1_000_000}, {"KB", 1_000},
		{"B", 1},
	}
	for _, m := range multipliers {
		if strings.HasSuffix(s, m.suffix) {
			num := strings.TrimSuffix(s, m.suffix)
			v, _ := parseFloat(strings.TrimSpace(num))
			return uint64(v * float64(m.mult))
		}
	}
	v, _ := parseFloat(s)
	return uint64(v)
}

func parseFloat(s string) (float64, error) {
	var v float64
	_, err := fmt.Sscanf(s, "%f", &v)
	return v, err
}

func (d *Docker) ImageExists(ctx context.Context, image string) (bool, error) {
	out, _, err := d.run(ctx, "image", "inspect", image)
	if err != nil {
		// `docker image inspect` exits non-zero when missing; treat that as "not exists".
		if exitErr := (&exec.ExitError{}); errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("docker image inspect %s: %w", image, err)
	}
	return strings.Contains(out, image) || strings.TrimSpace(out) != "[]", nil
}

func (d *Docker) Status(ctx context.Context, name string) (ContainerStatus, error) {
	out, _, err := d.run(ctx, "inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return StatusMissing, nil
		}
		return StatusErrored, fmt.Errorf("docker inspect %s: %w", name, err)
	}
	s := strings.TrimSpace(out)
	switch s {
	case "running":
		return StatusRunning, nil
	case "created":
		return StatusCreated, nil
	case "exited":
		return StatusExited, nil
	case "paused", "restarting", "removing", "dead":
		return StatusStopped, nil
	default:
		return ContainerStatus(s), nil
	}
}

func (d *Docker) Inspect(ctx context.Context, name string) (Health, error) {
	out, _, err := d.run(ctx, "inspect", "-f",
		"{{.State.OOMKilled}}|{{.RestartCount}}|{{.State.ExitCode}}", name)
	if err != nil {
		return Health{}, nil // missing/unavailable: best-effort, no health
	}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) != 3 {
		return Health{}, nil
	}
	h := Health{OOMKilled: parts[0] == "true"}
	fmt.Sscanf(parts[1], "%d", &h.RestartCount)
	fmt.Sscanf(parts[2], "%d", &h.ExitCode)
	return h, nil
}

func (d *Docker) CreateContainer(ctx context.Context, req CreateRequest) (string, error) {
	args := []string{"run", "-d", "--name", req.Name, "--restart", "unless-stopped"}
	if req.Network != "" {
		args = append(args, "--network", req.Network)
	}
	for k, v := range req.Env {
		args = append(args, "-e", k+"="+v)
	}
	for k, v := range req.Labels {
		args = append(args, "--label", k+"="+v)
	}
	for _, h := range req.ExtraHosts {
		args = append(args, "--add-host", h)
	}
	for _, vol := range req.Volumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", vol.Name, vol.Target))
	}
	for _, bm := range req.BindMounts {
		spec := fmt.Sprintf("%s:%s", bm.HostPath, bm.ContainerPath)
		if bm.ReadOnly {
			spec += ":ro"
		}
		args = append(args, "-v", spec)
	}
	if req.Memory != "" {
		args = append(args, "--memory", req.Memory)
	}
	if req.CPUs != "" {
		args = append(args, "--cpus", req.CPUs)
	}
	if req.StorageSize != "" {
		args = append(args, "--storage-opt", "size="+req.StorageSize)
	}
	if req.OOMScoreAdj != nil {
		args = append(args, "--oom-score-adj", strconv.Itoa(*req.OOMScoreAdj))
	}
	args = append(args, req.Image)
	args = append(args, req.Command...)

	out, err := d.runOK(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// UpdateResources applies a live memory-limit change via `docker update`. Empty
// memory is a no-op (nothing to change). oom-score-adj has no live equivalent —
// callers recreate the container to apply a priority change.
func (d *Docker) UpdateResources(ctx context.Context, name, memory string) error {
	if memory == "" {
		return nil
	}
	_, err := d.runOK(ctx, "update", "--memory", memory, name)
	return err
}

func (d *Docker) CopyToContainer(ctx context.Context, name, hostPath, containerPath string) error {
	_, err := d.runOK(ctx, "cp", hostPath, name+":"+containerPath)
	return err
}

func (d *Docker) CopyFromContainer(ctx context.Context, name, containerPath, hostPath string) error {
	_, err := d.runOK(ctx, "cp", name+":"+containerPath, hostPath)
	return err
}

func (d *Docker) Logs(ctx context.Context, name string, follow bool) (io.ReadCloser, error) {
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	args = append(args, name)
	cmd := exec.CommandContext(ctx, d.bin(), args...)
	r, w := io.Pipe()
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		return nil, err
	}
	go func() {
		_ = cmd.Wait()
		_ = w.Close()
	}()
	return &cmdReadCloser{cmd: cmd, reader: r}, nil
}

// BuildImage builds tag from contextDir using dockerfile (a path relative to
// contextDir). The returned stream carries combined build output; when the
// build fails the stream's final Read returns the build error instead of EOF
// (via CloseWithError), so callers distinguish success from failure without a
// side channel.
func (d *Docker) BuildImage(ctx context.Context, contextDir, dockerfile, tag string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, d.bin(), "build", "-t", tag,
		"-f", filepath.Join(contextDir, dockerfile), contextDir)
	r, w := io.Pipe()
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		_ = w.Close()
		return nil, err
	}
	go func() {
		if err := cmd.Wait(); err != nil {
			w.CloseWithError(fmt.Errorf("docker build failed: %w", err))
			return
		}
		_ = w.Close()
	}()
	return &cmdReadCloser{cmd: cmd, reader: r}, nil
}

// cmdReadCloser is an io.ReadCloser backed by a running exec.Cmd whose stdout
// has been wired into an io.PipeReader. Close kills the underlying process.
type cmdReadCloser struct {
	cmd    *exec.Cmd
	reader io.ReadCloser
}

func (c *cmdReadCloser) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *cmdReadCloser) Close() error {
	_ = c.reader.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
	return nil
}

func (d *Docker) StopContainer(ctx context.Context, name string) error {
	_, err := d.runOK(ctx, "stop", name)
	return err
}

func (d *Docker) StartContainer(ctx context.Context, name string) error {
	_, err := d.runOK(ctx, "start", name)
	return err
}

func (d *Docker) RemoveContainer(ctx context.Context, name string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, name)
	_, _, err := d.run(ctx, args...) // tolerate missing
	return err
}

func (d *Docker) Exec(ctx context.Context, name string, cmd []string) (string, string, int, error) {
	args := append([]string{"exec", name}, cmd...)
	command := exec.CommandContext(ctx, d.bin(), args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit = exitErr.ExitCode()
			return stdout.String(), stderr.String(), exit, nil
		}
		return stdout.String(), stderr.String(), -1, err
	}
	return stdout.String(), stderr.String(), exit, nil
}

// ExecStream runs a command inside a container and streams its combined output.
// Modeled on Logs: the command's stdout/stderr feed an io.Pipe the caller reads.
func (d *Docker) ExecStream(ctx context.Context, name string, cmd []string) (io.ReadCloser, error) {
	args := append([]string{"exec", name}, cmd...)
	command := exec.CommandContext(ctx, d.bin(), args...)
	r, w := io.Pipe()
	command.Stdout = w
	command.Stderr = w
	if err := command.Start(); err != nil {
		_ = w.Close()
		return nil, err
	}
	go func() {
		_ = command.Wait()
		_ = w.Close()
	}()
	return &cmdReadCloser{cmd: command, reader: r}, nil
}

// Ensure Docker satisfies Runtime at compile time.
var _ Runtime = (*Docker)(nil)
