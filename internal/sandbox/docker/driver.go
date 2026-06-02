// Package docker is the Docker driver for internal/sandbox. It maps
// each Sandbox operation onto a thin shell over the official
// github.com/moby/moby/client SDK (the modern modular replacement for
// the legacy github.com/docker/docker/client; same daemon, same API,
// new Go package layout as of moby v28).
//
// One Driver holds one *client.Client and is safe for concurrent use.
// Beyond the client handle the driver is stateless — Handles are
// container IDs, looked up on the daemon as needed.
//
// v1 scope (SPEC §7.2): lifecycle + exec. Logs/CopyIn/CopyOut are
// stubbed and return sandbox.ErrNotSupported so callers can fall back
// portably. Snapshot/Restore also return ErrNotSupported — Docker
// has no equivalent of microVM snapshots.
package docker

import (
	"context"
	"fmt"
	"io"
	"strconv"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Driver implements sandbox.Sandbox against the Docker daemon.
type Driver struct {
	cli           *client.Client
	allowedRoots  []string
}

// Option configures a Driver at construction time.
type Option func(*Driver)

// WithAllowedMountRoots sets the host directories whose contents may
// be mounted into sandboxes. Mount sources outside these roots are
// rejected by sandbox.ValidateSpec before reaching the Docker daemon
// — this is how we enforce SPEC §3 (sessions never cross host
// boundaries) at the driver level.
//
// An empty list means no mounts are allowed.
func WithAllowedMountRoots(roots ...string) Option {
	return func(d *Driver) { d.allowedRoots = roots }
}

// New connects to the Docker daemon using DOCKER_HOST env config
// (falls back to the platform default — unix:///var/run/docker.sock
// on Unix). The client negotiates API version with the server.
func New(opts ...Option) (*Driver, error) {
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	d := &Driver{cli: cli}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

// Close releases the underlying HTTP client. Safe to call once.
func (d *Driver) Close() error {
	return d.cli.Close()
}

// Compile-time checks: the Driver is a full Sandbox and also provides
// the optional Pinger / ImageManager capabilities the session layer
// type-asserts for preflight and image readiness.
var (
	_ sandbox.Sandbox      = (*Driver)(nil)
	_ sandbox.Pinger       = (*Driver)(nil)
	_ sandbox.ImageManager = (*Driver)(nil)
)

// Ping implements sandbox.Pinger. It returns the daemon's negotiated
// API version when reachable, and an ErrUnavailable-wrapped error when
// the daemon can't be reached (not running, socket gone, etc).
func (d *Driver) Ping(ctx context.Context) (string, error) {
	res, err := d.cli.Ping(ctx, client.PingOptions{})
	if err != nil {
		return "", normalizeErr(err)
	}
	return res.APIVersion, nil
}

// ImageExists implements sandbox.ImageManager. A daemon "not found"
// for the image is a clean false; any other failure propagates.
func (d *Driver) ImageExists(ctx context.Context, ref string) (bool, error) {
	if _, err := d.cli.ImageInspect(ctx, ref); err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, normalizeErr(err)
	}
	return true, nil
}

// ImagePull implements sandbox.ImageManager. It blocks until the pull
// completes, invoking onProgress (if non-nil) with aggregate progress
// derived from the daemon's per-layer JSON stream.
func (d *Driver) ImagePull(ctx context.Context, ref string, onProgress func(sandbox.PullProgress)) error {
	resp, err := d.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return normalizeErr(err)
	}
	defer resp.Close()

	// Aggregate across layers: keep each layer's latest current/total
	// (keyed by id) and sum. Total stays 0 until at least one layer
	// reports a size, which the UI renders as an indeterminate bar.
	layers := map[string]jsonstream.Progress{}
	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return normalizeErr(err)
		}
		if msg.Error != nil {
			return fmt.Errorf("pull %s: %s", ref, msg.Error.Message)
		}
		if onProgress == nil {
			continue
		}
		if msg.ID != "" && msg.Progress != nil && msg.Progress.Total > 0 {
			layers[msg.ID] = *msg.Progress
		}
		var cur, tot int64
		for _, p := range layers {
			cur += p.Current
			tot += p.Total
		}
		onProgress(sandbox.PullProgress{Status: msg.Status, Current: cur, Total: tot})
	}
	return nil
}

// Create makes a container from spec but does not start it. Returns
// the container ID as a sandbox.Handle.
func (d *Driver) Create(ctx context.Context, spec sandbox.SandboxSpec) (sandbox.Handle, error) {
	if err := sandbox.ValidateSpec(spec, d.allowedRoots); err != nil {
		return "", err
	}

	// Surface a missing image as a distinct, actionable error up front
	// rather than letting ContainerCreate bury it under the daemon's
	// generic "No such image" string — the session layer turns this
	// into a 422 so the UI can offer to download it. This also
	// normalizes a down daemon to ErrUnavailable here.
	if exists, err := d.ImageExists(ctx, spec.Image); err != nil {
		return "", err
	} else if !exists {
		return "", fmt.Errorf("%w: %s", sandbox.ErrImageNotFound, spec.Image)
	}

	cfg := &container.Config{
		Image: spec.Image,
		Cmd:   spec.Cmd,
		Env:   envList(spec.Env),
	}

	netMode, err := toNetworkMode(spec.Network.Mode)
	if err != nil {
		return "", err
	}
	hostCfg := &container.HostConfig{
		Mounts:       toMounts(spec.Mounts),
		Resources:    toResources(spec.Resources),
		PortBindings: toPortBindings(spec.Network.Ports),
		NetworkMode:  netMode,
	}

	resp, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           cfg,
		HostConfig:       hostCfg,
		NetworkingConfig: &network.NetworkingConfig{},
	})
	if err != nil {
		return "", normalizeErr(err)
	}
	return sandbox.Handle(resp.ID), nil
}

func (d *Driver) Start(ctx context.Context, h sandbox.Handle) error {
	_, err := d.cli.ContainerStart(ctx, string(h), client.ContainerStartOptions{})
	return normalizeErr(err)
}

func (d *Driver) Stop(ctx context.Context, h sandbox.Handle) error {
	// Empty options → Docker default (SIGTERM, 10s grace, then SIGKILL).
	_, err := d.cli.ContainerStop(ctx, string(h), client.ContainerStopOptions{})
	return normalizeErr(err)
}

func (d *Driver) Destroy(ctx context.Context, h sandbox.Handle) error {
	// Force removes a running container in one shot. Anonymous volumes
	// attached to the container are removed; bind mounts (host paths)
	// stay on the host.
	_, err := d.cli.ContainerRemove(ctx, string(h), client.ContainerRemoveOptions{
		RemoveVolumes: true,
		Force:         true,
	})
	return normalizeErr(err)
}

// Exec creates an exec instance in the container and attaches to it.
// The returned Process owns the hijacked connection; callers must
// Wait or Close it (forgetting both leaks the connection).
func (d *Driver) Exec(ctx context.Context, h sandbox.Handle, cmd sandbox.ExecSpec) (sandbox.Process, error) {
	// AttachStdin is true whenever the caller might want to write
	// input: explicitly via ExecSpec.Stdin (one-shot input ready at
	// create time) OR interactively when a TTY is requested
	// (caller writes via Process.Stdin() after exec starts). Without
	// AttachStdin true, the daemon doesn't connect the exec's stdin
	// to our hijacked stream — writes go to /dev/null.
	createResp, err := d.cli.ExecCreate(ctx, string(h), client.ExecCreateOptions{
		Cmd:          cmd.Cmd,
		Env:          envList(cmd.Env),
		WorkingDir:   cmd.WorkingDir,
		TTY:          cmd.Tty,
		AttachStdin:  cmd.Stdin != nil || cmd.Tty,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, normalizeErr(err)
	}

	attachResp, err := d.cli.ExecAttach(ctx, createResp.ID, client.ExecAttachOptions{
		TTY: cmd.Tty,
	})
	if err != nil {
		return nil, normalizeErr(err)
	}

	return newProcess(d.cli, createResp.ID, attachResp.HijackedResponse, cmd.Stdin, cmd.Tty), nil
}

// Logs, CopyIn, CopyOut — stubs for the next pass. Return
// ErrNotSupported so callers can detect with errors.Is and fall back.

func (d *Driver) Logs(ctx context.Context, h sandbox.Handle, opts sandbox.LogOpts) (io.ReadCloser, error) {
	return nil, sandbox.ErrNotSupported
}

func (d *Driver) CopyIn(ctx context.Context, h sandbox.Handle, src, dst string) error {
	return sandbox.ErrNotSupported
}

func (d *Driver) CopyOut(ctx context.Context, h sandbox.Handle, src, dst string) error {
	return sandbox.ErrNotSupported
}

// Snapshot / Restore: Docker has no microVM-snapshot equivalent. The
// session layer degrades to a fresh sandbox per SPEC §7.1.

func (d *Driver) Snapshot(ctx context.Context, h sandbox.Handle) (sandbox.SnapshotID, error) {
	return "", sandbox.ErrNotSupported
}

func (d *Driver) Restore(ctx context.Context, h sandbox.Handle, id sandbox.SnapshotID) error {
	return sandbox.ErrNotSupported
}

// RemoveStorage deletes a named Docker volume. Force=true so a
// volume still attached to a (stopped) container is removed without
// requiring a separate detach step — the session layer always
// removes the container first, so the attach window should be
// closed, but Force keeps us robust against races.
func (d *Driver) RemoveStorage(ctx context.Context, name string) error {
	_, err := d.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true})
	return normalizeErr(err)
}

// ---- helpers ------------------------------------------------------------

// normalizeErr maps moby's errdefs-tagged errors to sandbox sentinels.
// Callers use errors.Is(err, sandbox.ErrNotFound) etc. instead of
// poking at the underlying Docker error types.
func normalizeErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case client.IsErrConnectionFailed(err) || cerrdefs.IsUnavailable(err):
		return fmt.Errorf("%w: %v", sandbox.ErrUnavailable, err)
	case cerrdefs.IsNotFound(err):
		return fmt.Errorf("%w: %v", sandbox.ErrNotFound, err)
	case cerrdefs.IsConflict(err):
		return fmt.Errorf("%w: %v", sandbox.ErrConflict, err)
	}
	return err
}

// envList flattens map[string]string to Docker's KEY=VALUE slice.
// Order is unspecified — Docker doesn't care.
func envList(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func toMounts(ms []sandbox.Mount) []mount.Mount {
	if len(ms) == 0 {
		return nil
	}
	out := make([]mount.Mount, 0, len(ms))
	for _, m := range ms {
		switch m.Type {
		case sandbox.MountBind:
			out = append(out, mount.Mount{
				Type:     mount.TypeBind,
				Source:   m.Source,
				Target:   m.Target,
				ReadOnly: m.ReadOnly,
			})
		case sandbox.MountVolume:
			// Named Docker volume. Auto-created on container create
			// if it doesn't exist, persists across container removal
			// (container-removal opts only clear *anonymous* volumes),
			// and is explicitly cleaned up via RemoveStorage when
			// the session is destroyed.
			out = append(out, mount.Mount{
				Type:     mount.TypeVolume,
				Source:   m.Source,
				Target:   m.Target,
				ReadOnly: m.ReadOnly,
			})
		default:
			// MountCopyIn is a sandbox-layer concept that doesn't map
			// to a single Docker primitive (it would need a
			// copy-on-create flow). Silently drop — the validator
			// catches misuse via SandboxSpec validation.
		}
	}
	return out
}

// toResources converts sandbox.Resources to Docker's per-container
// resource caps. Zero values pass through unchanged — Docker reads
// zero as "no explicit limit". Resources.Disk is ignored: Docker disk
// quotas aren't portable across storage drivers.
func toResources(r sandbox.Resources) container.Resources {
	var out container.Resources
	if r.CPU > 0 {
		// NanoCPUs = cores * 1e9.
		out.NanoCPUs = int64(r.CPU * 1e9)
	}
	if r.Memory > 0 {
		out.Memory = r.Memory
	}
	return out
}

func toPortBindings(ports []sandbox.PortMapping) network.PortMap {
	if len(ports) == 0 {
		return nil
	}
	out := network.PortMap{}
	for _, p := range ports {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		key, err := network.ParsePort(fmt.Sprintf("%d/%s", p.Sandbox, proto))
		if err != nil {
			continue
		}
		out[key] = append(out[key], network.PortBinding{
			HostPort: strconv.Itoa(p.Host),
		})
	}
	return out
}

// toNetworkMode translates the abstract sandbox.NetworkMode into the
// Docker network-mode string the daemon expects.
func toNetworkMode(m sandbox.NetworkMode) (container.NetworkMode, error) {
	switch m {
	case sandbox.NetIsolated:
		return container.NetworkMode("none"), nil
	case sandbox.NetNAT:
		return container.NetworkMode("bridge"), nil
	case sandbox.NetHost:
		return container.NetworkMode("host"), nil
	default:
		return "", fmt.Errorf("docker: unknown NetworkMode %d", m)
	}
}

