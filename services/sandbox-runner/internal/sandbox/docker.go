package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// Docker creates sandboxes through the Docker Engine API.
type Docker struct {
	cli *client.Client
}

// NewDocker connects using DOCKER_HOST (default: the local socket) and
// negotiates the API version with the daemon.
func NewDocker() (*Docker, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Docker{cli: cli}, nil
}

func (d *Docker) Close() error { return d.cli.Close() }

// Ping checks the daemon is reachable (readiness).
func (d *Docker) Ping(ctx context.Context) error {
	_, err := d.cli.Ping(ctx)
	return err
}

// EnsureNetwork creates the egress bridge network used during dependency
// installation, if it does not exist yet.
func (d *Docker) EnsureNetwork(ctx context.Context, name string) error {
	_, err := d.cli.NetworkInspect(ctx, name, network.InspectOptions{})
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return err
	}
	_, err = d.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{Label: "true"},
		// No inter-container traffic: sandboxes must not reach each other.
		Options: map[string]string{"com.docker.network.bridge.enable_icc": "false"},
	})
	if cerrdefs.IsConflict(err) {
		return nil // another runner created it concurrently
	}
	return err
}

// ImageExists reports whether the sandbox image is available locally.
func (d *Docker) ImageExists(ctx context.Context, image string) (bool, error) {
	_, err := d.cli.ImageInspect(ctx, image)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// Container is one running sandbox.
type Container struct {
	ID      string
	network string
	cli     *client.Client
}

// Start creates and starts a sandbox. The caller must call Remove.
func (d *Docker) Start(ctx context.Context, s Spec) (*Container, error) {
	created, err := d.cli.ContainerCreate(ctx, ContainerConfig(s), HostConfig(s), nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	c := &Container{ID: created.ID, network: s.Network, cli: d.cli}
	if err := d.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = c.Remove(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("start sandbox: %w", err)
	}
	return c, nil
}

// Remove force-removes the container. It uses its own short deadline so it
// still runs when the validation's context has already expired.
func (c *Container) Remove(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := c.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// DisconnectNetwork cuts the sandbox off from the egress network. After this
// the container has only a loopback interface.
func (c *Container) DisconnectNetwork(ctx context.Context) error {
	if c.network == NetworkNone || c.network == "" {
		return nil
	}
	if err := c.cli.NetworkDisconnect(ctx, c.network, c.ID, true); err != nil {
		return fmt.Errorf("disconnect sandbox network: %w", err)
	}
	c.network = NetworkNone
	return nil
}

// ExecResult is a finished command.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
	Duration time.Duration
}

// Output caps: enough for any tool's JSON report, small enough to bound memory.
const maxOutputBytes = 8 << 20

// Exec runs cmd inside the sandbox under `timeout`, which sends TERM at the
// deadline and KILL five seconds later. stdin, if non-nil, is streamed in.
func (c *Container) Exec(ctx context.Context, cmd []string, limit time.Duration, stdin io.Reader) (ExecResult, error) {
	secs := max(int(limit.Seconds()), 1)
	argv := append([]string{"timeout", "--kill-after=5", strconv.Itoa(secs)}, cmd...)
	started := time.Now()

	exec, err := c.cli.ContainerExecCreate(ctx, c.ID, container.ExecOptions{
		Cmd: argv, User: User, WorkingDir: "/workspace",
		AttachStdout: true, AttachStderr: true, AttachStdin: stdin != nil,
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec create: %w", err)
	}
	attach, err := c.cli.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec attach: %w", err)
	}
	defer attach.Close()

	// Unblock the reader if our context ends before the command does.
	stop := context.AfterFunc(ctx, attach.Close)
	defer stop()

	if stdin != nil {
		go func() {
			_, _ = io.Copy(attach.Conn, stdin)
			_ = attach.CloseWrite()
		}()
	}
	stdout, stderr := &cappedBuffer{limit: maxOutputBytes}, &cappedBuffer{limit: maxOutputBytes}
	if _, err := stdcopy.StdCopy(stdout, stderr, attach.Reader); err != nil && ctx.Err() == nil {
		return ExecResult{}, fmt.Errorf("exec read: %w", err)
	}
	if ctx.Err() != nil {
		return ExecResult{TimedOut: true, Duration: time.Since(started)}, ctx.Err()
	}

	inspect, err := c.cli.ContainerExecInspect(ctx, exec.ID)
	if err != nil {
		return ExecResult{}, fmt.Errorf("exec inspect: %w", err)
	}
	took := time.Since(started)
	return ExecResult{
		ExitCode: inspect.ExitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		// 124: `timeout` sent TERM. 137 (SIGKILL) is ambiguous: `timeout`
		// escalating after the grace period, or the kernel's OOM killer. Only
		// the former happens after the limit has elapsed.
		TimedOut: inspect.ExitCode == 124 || (inspect.ExitCode == 137 && took >= limit),
		Duration: took,
	}, nil
}

// CopyDir streams dir (minus .git) into /workspace as a tar archive.
// docker cp cannot write into a tmpfs mount, so we pipe through `tar -x`.
func (c *Container) CopyDir(ctx context.Context, dir string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTar(pw, dir)) }()
	res, err := c.Exec(ctx, []string{"tar", "-x", "-f", "-", "-C", "/workspace", "--no-same-owner"}, 2*time.Minute, pr)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("copy workspace: tar exited %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}

// ReadFile returns a file's contents from inside the sandbox ("" if absent).
func (c *Container) ReadFile(ctx context.Context, path string) (string, error) {
	res, err := c.Exec(ctx, []string{"cat", path}, 30*time.Second, nil)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", nil
	}
	return res.Stdout, nil
}

func writeTar(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 10001, 10001, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path) //nolint:gosec // walking our own checkout
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			_ = f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Reap removes sandbox containers created before cutoff: leftovers from a
// runner that crashed mid-validation. Returns how many were removed.
func (d *Docker) Reap(ctx context.Context, olderThan time.Duration) (int, error) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		All: true, Filters: filters.NewArgs(filters.Arg("label", Label+"=true")),
	})
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan).Unix()
	removed := 0
	var errs []error
	for _, c := range list {
		if c.Created > cutoff {
			continue
		}
		err := d.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true})
		if err != nil && !cerrdefs.IsNotFound(err) {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// cappedBuffer keeps the first limit bytes and silently drops the rest.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	if b.buf.Len() >= b.limit {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
