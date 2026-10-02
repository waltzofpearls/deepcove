package vm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// newTestDriver builds a Driver whose image store is fed a synthetic image, so Create can be exercised
// without a network or a VM.
func newTestDriver(t *testing.T) *Driver {
	t.Helper()
	dir := t.TempDir()
	// Real files: New fingerprints the guest init's bytes to key the image cache, so it has to be
	// readable. Failing at construction is deliberate, since a missing init means no VM can ever boot.
	kernel, guestInit := filepath.Join(dir, "vmlinux"), filepath.Join(dir, "init")
	for _, f := range []string{kernel, guestInit} {
		if err := os.WriteFile(f, []byte("stub "+filepath.Base(f)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d, err := NewDriver(&fakeBackend{}, kernel, guestInit, dir)
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	img := layerImage(t, map[string]string{"etc/hostname": "deepcove\n"})
	d.images = &imageStore{
		dir:           dir,
		platform:      guestPlatform(),
		resolveDigest: func(context.Context, string) (string, error) { return "sha256:test", nil },
		pull:          func(context.Context, string, v1.Platform) (v1.Image, error) { return img, nil },
	}
	return d
}

func TestNewDriverRejectsMissingPaths(t *testing.T) {
	cases := []struct {
		name      string
		kernel    string
		guestInit string
	}{
		{"no kernel", "", "/init"},
		{"no guest init", "/vmlinux", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewDriver(&fakeBackend{}, tc.kernel, tc.guestInit, t.TempDir()); err == nil {
				t.Error("expected an error for an incomplete driver configuration")
			}
		})
	}
}

func TestCreateBuildsTheBaseImageAndReturnsAHandle(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "ghcr.io/x/agent:latest", Cmd: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if h == "" {
		t.Error("Create must return a non empty handle")
	}
	s, err := d.lookup(h)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	assertExt4(t, s.baseImage)
}

func TestCreateRejectsASpecWithNoImage(t *testing.T) {
	d := newTestDriver(t)
	if _, err := d.Create(context.Background(), sandbox.SandboxSpec{}); err == nil {
		t.Error("a spec without an image must be rejected")
	}
}

// The entrypoint is what the guest runs; without it a VM would boot to nothing, so the driver rejects
// the spec rather than starting a session that can never work.
func TestCreateRequiresAnEntrypoint(t *testing.T) {
	d := newTestDriver(t)
	if _, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "img:tag"}); err == nil {
		t.Error("a spec without Cmd must be rejected")
	}
}

// Start shares the stored folders without checking them again, so Create and Restart are the only gate
// between a spec and the host's files.
func TestDriver_RefusesAFolderOutsideTheAllowedRoots(t *testing.T) {
	cmd := []string{"sh", "-c", "true"}
	outside := sandbox.SandboxSpec{
		Image:  "img:tag",
		Cmd:    cmd,
		Mounts: []sandbox.Mount{{Source: "/etc", Target: "/workspace"}},
	}

	t.Run("create", func(t *testing.T) {
		d := newTestDriver(t)
		d.shareRoots = sandbox.ShareRoots{Allowed: []string{"/Users/me"}}
		if _, err := d.Create(context.Background(), outside); err == nil || !strings.Contains(err.Error(), "only folders inside") {
			t.Errorf("error = %v, want the folder refused as outside the allowed roots", err)
		}
	})

	t.Run("restart", func(t *testing.T) {
		d := newTestDriver(t)
		d.shareRoots = sandbox.ShareRoots{Allowed: []string{"/Users/me"}}
		id, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "img:tag", Cmd: cmd})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := d.Restart(context.Background(), id, outside); err == nil || !strings.Contains(err.Error(), "only folders inside") {
			t.Errorf("error = %v, want the folder refused as outside the allowed roots", err)
		}
		// Refused before anything changed, so the next Start still shares only what was checked.
		state, err := d.lookup(id)
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if len(state.spec.Mounts) != 0 {
			t.Errorf("the refused folders were stored anyway: %v", state.spec.Mounts)
		}
	})
}

func TestDriver_RefusesAShareUnderTheGuestHome(t *testing.T) {
	cases := []struct {
		target      string
		wantRefused bool
	}{
		{target: "/root", wantRefused: true},
		{target: "/root/.ssh", wantRefused: true},
		{target: "/root/../root/x", wantRefused: true},
		{target: "/rootless", wantRefused: false},
		{target: "/workspace", wantRefused: false},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			d := newTestDriver(t)
			d.shareRoots = sandbox.ShareRoots{Allowed: []string{"/Users/me"}}
			_, err := d.Create(context.Background(), sandbox.SandboxSpec{
				Image:  "img:tag",
				Cmd:    []string{"sh", "-c", "true"},
				Mounts: []sandbox.Mount{{Source: "/Users/me/code", Target: tc.target}},
			})
			refused := err != nil && strings.Contains(err.Error(), "agent's home")
			if refused != tc.wantRefused {
				t.Errorf("error = %v, want refused %v", err, tc.wantRefused)
			}
		})
	}
}

func TestDriver_Start_RechecksTheShares(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, "data")
	project := filepath.Join(home, "project")
	for _, dir := range []string{dataDir, project} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d := newTestDriver(t)
	d.shareRoots = sandbox.ShareRoots{Allowed: []string{home}, Refused: []string{dataDir}}
	id, err := d.Create(context.Background(), sandbox.SandboxSpec{
		Image:  "img:tag",
		Cmd:    []string{"sh", "-c", "true"},
		Mounts: []sandbox.Mount{{Source: project, Target: "/workspace"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// What a guest that can write next to the share could do while the session is stopped.
	if err := os.Remove(project); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dataDir, project); err != nil {
		t.Fatal(err)
	}

	if err := d.Start(context.Background(), id); err == nil || !strings.Contains(err.Error(), "deepcove's own files") {
		t.Errorf("Start error = %v, want the swapped folder refused", err)
	}
}

func TestHandlesAreUnique(t *testing.T) {
	d := newTestDriver(t)
	spec := sandbox.SandboxSpec{Image: "ghcr.io/x/agent:latest", Cmd: []string{"sh", "-c", "true"}}
	first, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := d.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if first == second {
		t.Error("each sandbox must get its own handle, even for the same image")
	}
}

// Operations on a handle that was never created must say so rather than panic on a nil lookup.
func TestUnknownHandleIsNotFound(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()
	const ghost = sandbox.ID("applevf-does-not-exist")

	if err := d.Start(ctx, ghost); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("Start error = %v, want ErrNotFound", err)
	}
	if err := d.Stop(ctx, ghost); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("Stop error = %v, want ErrNotFound", err)
	}
	if _, err := d.Exec(ctx, ghost, sandbox.ExecSpec{Cmd: []string{"/bin/true"}}); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("Exec error = %v, want ErrNotFound", err)
	}
}

// Exec against a created but unstarted sandbox is a lifecycle conflict, not a missing sandbox.
func TestExecBeforeStartIsAConflict(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "img:tag", Cmd: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := d.Exec(context.Background(), h, sandbox.ExecSpec{Cmd: []string{"/bin/true"}}); !errors.Is(err, sandbox.ErrConflict) {
		t.Errorf("error = %v, want ErrConflict", err)
	}
}

// Stopping a sandbox that never started is a no op, so session teardown does not have to track state.
func TestStopWithoutStartIsANoOp(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "img:tag", Cmd: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.Stop(context.Background(), h); err != nil {
		t.Errorf("Stop on an unstarted sandbox = %v, want nil", err)
	}
}

func TestDestroyForgetsTheSandbox(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{Image: "img:tag", Cmd: []string{"sh", "-c", "true"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.Destroy(context.Background(), h); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := d.lookup(h); !errors.Is(err, sandbox.ErrNotFound) {
		t.Errorf("the sandbox should be gone after Destroy, got %v", err)
	}
}

// A VM needs concrete CPU and memory values, so an unset limit has to become a sensible default rather
// than zero.
func TestResourceDefaults(t *testing.T) {
	if got := cpuCount(sandbox.Resources{}); got != defaultCPUCount {
		t.Errorf("cpuCount(zero) = %d, want %d", got, defaultCPUCount)
	}
	if got := memoryBytes(sandbox.Resources{}); got != defaultMemoryBytes {
		t.Errorf("memoryBytes(zero) = %d, want %d", got, defaultMemoryBytes)
	}
	if got := cpuCount(sandbox.Resources{CPU: 4}); got != 4 {
		t.Errorf("cpuCount(4) = %d, want 4", got)
	}
	if got := memoryBytes(sandbox.Resources{Memory: 512 << 20}); got != 512<<20 {
		t.Errorf("memoryBytes(512MiB) = %d", got)
	}
}

// The unimplemented optional operations must report ErrNotSupported so the session layer can treat them
// as a capability signal rather than a failure.
func TestUnsupportedOperations(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()

	if _, err := d.Snapshot(ctx, "h"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("Snapshot = %v, want ErrNotSupported", err)
	}
	if err := d.Restore(ctx, "h", "snap"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("Restore = %v, want ErrNotSupported", err)
	}
	if err := d.CopyIn(ctx, "h", "/a", "/b"); !errors.Is(err, sandbox.ErrNotSupported) {
		t.Errorf("CopyIn = %v, want ErrNotSupported", err)
	}
}

func TestImageExistsReflectsTheCache(t *testing.T) {
	d := newTestDriver(t)
	ctx := context.Background()

	exists, err := d.ImageExists(ctx, "img:tag")
	if err != nil {
		t.Fatalf("ImageExists: %v", err)
	}
	if exists {
		t.Error("nothing is cached before the first build")
	}

	if err := d.ImagePull(ctx, "img:tag", nil); err != nil {
		t.Fatalf("ImagePull: %v", err)
	}
	exists, err = d.ImageExists(ctx, "img:tag")
	if err != nil {
		t.Fatalf("ImageExists after pull: %v", err)
	}
	if !exists {
		t.Error("the image should be cached after a pull")
	}
}

// The registry is the only record these VMs have, so listing it is the only way to see them at all.
func TestListSandboxes(t *testing.T) {
	d := &Driver{
		sandboxes: map[sandbox.ID]*sandboxState{
			"older": {
				spec:      sandbox.SandboxSpec{Image: "alpine:3.20"},
				createdAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			},
			"newer": {
				spec:      sandbox.SandboxSpec{Image: "debian:13", Resources: sandbox.Resources{CPU: 4, Memory: 8 << 30}},
				createdAt: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
				machine:   &Guest{},
			},
		},
	}

	got, err := d.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d instances, want 2", len(got))
	}

	// Newest first, so a poll does not reshuffle the view and what you just made is at the top.
	if got[0].ID != "newer" || got[1].ID != "older" {
		t.Errorf("order = %q, %q; want newer, older", got[0].ID, got[1].ID)
	}
	// A booted VM is running; one that was created but never started is not.
	if got[0].State != sandbox.InstanceRunning {
		t.Errorf("started sandbox state = %q, want running", got[0].State)
	}
	if got[1].State != sandbox.InstanceStopped {
		t.Errorf("unstarted sandbox state = %q, want stopped", got[1].State)
	}
	// Resources report what the VM actually got, so an explicit request comes back as asked...
	if got[0].Resources.CPU != 4 || got[0].Resources.Memory != 8<<30 {
		t.Errorf("explicit resources = %+v, want 4 CPU / 8GiB", got[0].Resources)
	}
	// ...and a request that named nothing reports the defaults that were applied, not zero.
	if got[1].Resources.CPU != float64(defaultCPUCount) || got[1].Resources.Memory != int64(defaultMemoryBytes) {
		t.Errorf("default resources = %+v, want the driver defaults", got[1].Resources)
	}
	// An uncapped writable layer is not unlimited: tmpfs defaults to half the RAM, so that is the real
	// cap and reporting zero would read as "no limit".
	if got[1].Resources.Disk != int64(defaultMemoryBytes)/2 {
		t.Errorf("default disk = %d, want half of memory (%d)", got[1].Resources.Disk, int64(defaultMemoryBytes)/2)
	}
	// The derived cap follows each sandbox's OWN memory, not the default.
	if got[0].Resources.Disk != (8<<30)/2 {
		t.Errorf("disk for an 8GiB sandbox = %d, want half of its own memory", got[0].Resources.Disk)
	}
	if got[1].Image != "alpine:3.20" {
		t.Errorf("image = %q, want alpine:3.20", got[1].Image)
	}
}

// The spec's folders were checked at create or restart, and Start shares them without checking again. A
// caller editing the list it was handed must not change what the VM gets.
func TestListSandboxes_HandsOutACopyOfTheMounts(t *testing.T) {
	shared := []sandbox.Mount{{Source: "/Users/me/code", Target: "/workspace"}}
	d := &Driver{sandboxes: map[sandbox.ID]*sandboxState{"vm": {spec: sandbox.SandboxSpec{Mounts: shared}}}}

	got, err := d.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	got[0].Mounts[0].Source = "/etc"

	if source := d.sandboxes["vm"].spec.Mounts[0].Source; source != "/Users/me/code" {
		t.Errorf("the driver's own spec now shares %q", source)
	}
}

// fakeProcess records what was written to stdin and whether it was closed, which is the whole contract
// streamStdin has to honour.
type fakeProcess struct {
	sandbox.Process
	buf    bytes.Buffer
	closed bool
}

func (f *fakeProcess) Stdin() io.WriteCloser { return &fakeStdin{p: f} }

type fakeStdin struct{ p *fakeProcess }

func (s *fakeStdin) Write(b []byte) (int, error) { return s.p.buf.Write(b) }
func (s *fakeStdin) Close() error                { s.p.closed = true; return nil }

// A command reading to EOF never exits unless stdin is CLOSED after the data, so both halves matter.
// Dropping the payload is what broke file upload on this runtime.
func TestStreamStdin(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"a payload arrives intact", "hello upload"},
		{"an empty payload still closes, so the command can exit", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc := &fakeProcess{}
			streamStdin(proc, strings.NewReader(tc.in))

			if got := proc.buf.String(); got != tc.in {
				t.Errorf("stdin = %q, want %q", got, tc.in)
			}
			if !proc.closed {
				t.Error("stdin was never closed, so a command reading to EOF would hang forever")
			}
		})
	}
}

// A driver whose process cannot accept stdin must not panic on a nil writer.
func TestStreamStdinWithoutAWriter(t *testing.T) {
	streamStdin(&noStdinProcess{}, strings.NewReader("data"))
}

type noStdinProcess struct{ sandbox.Process }

func (noStdinProcess) Stdin() io.WriteCloser { return nil }

// Mounts become virtio-fs shares.
func TestHostShares(t *testing.T) {
	shares, guest := hostShares([]sandbox.Mount{
		{Source: "/Users/me/code", Target: "/workspace"},
		{Source: "/Users/me/notes", Target: "/notes", ReadOnly: true},
	})

	if len(shares) != 2 || len(guest) != 2 {
		t.Fatalf("got %d shares / %d guest mounts, want 2 each", len(shares), len(guest))
	}
	if shares[0].HostPath != "/Users/me/code" || shares[0].ReadOnly {
		t.Errorf("share[0] = %+v", shares[0])
	}
	if !shares[1].ReadOnly {
		t.Error("share[1] should be read only")
	}
	// Tags must line up between the host device and what the guest is told to mount.
	for i := range shares {
		if shares[i].Tag != guest[i].Tag {
			t.Errorf("tag mismatch at %d: host %q, guest %q", i, shares[i].Tag, guest[i].Tag)
		}
	}
	if shares[0].Tag == shares[1].Tag {
		t.Error("every share needs its own tag")
	}
	if guest[0].Target != "/workspace" {
		t.Errorf("guest[0].Target = %q, want /workspace", guest[0].Target)
	}
}

// No mounts must not produce an empty device list that the VM config would still be handed.
func TestHostSharesWithNoMounts(t *testing.T) {
	shares, guest := hostShares(nil)
	if shares != nil || guest != nil {
		t.Errorf("got %v / %v, want nil for no mounts", shares, guest)
	}
}

// The home disk is named by session, so recreating the sandbox keeps the agent's home. Creating it twice
// must reuse the same file rather than start over.
func TestHomeDiskIsPerSessionAndReused(t *testing.T) {
	d := &Driver{disks: t.TempDir()}
	spec := sandbox.SandboxSpec{SessionID: "sess-1"}

	first, err := d.homeDisk(spec)
	if err != nil {
		t.Fatalf("homeDisk: %v", err)
	}
	if first == "" {
		t.Fatal("no disk created for a spec with a session id")
	}
	// Something the agent wrote. A second call must not wipe it.
	if err := os.WriteFile(first, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := d.homeDisk(spec)
	if err != nil {
		t.Fatalf("homeDisk again: %v", err)
	}
	if second != first {
		t.Errorf("second call returned %q, want the same disk %q", second, first)
	}
	if body, _ := os.ReadFile(first); string(body) != "state" {
		t.Error("the existing disk was recreated, losing its contents")
	}
}

func TestHomeDiskSkippedWithoutASession(t *testing.T) {
	d := &Driver{disks: t.TempDir()}

	// No session id: nothing stable to name storage after.
	if got, err := d.homeDisk(sandbox.SandboxSpec{}); err != nil || got != "" {
		t.Errorf("got %q, %v; want no disk without a session id", got, err)
	}
}

func TestRemoveStorageDeletesTheDisk(t *testing.T) {
	d := &Driver{disks: t.TempDir()}
	spec := sandbox.SandboxSpec{SessionID: "sess-2"}
	path, err := d.homeDisk(spec)
	if err != nil {
		t.Fatalf("homeDisk: %v", err)
	}

	if err := d.RemoveStorage(context.Background(), "sess-2"); err != nil {
		t.Fatalf("RemoveStorage: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the disk survived RemoveStorage")
	}
	// Removing again is how a repeated destroy behaves, and must not be an error.
	if err := d.RemoveStorage(context.Background(), "sess-2"); err != nil {
		t.Errorf("second RemoveStorage: %v", err)
	}
}

// Swap from a previous process is dead weight: those VMs are gone. A session's home disk sitting in the
// same directory is not, so the sweep has to tell them apart.
func TestNewSweepsStaleSwapDisks(t *testing.T) {
	stateDir := t.TempDir()
	disks := filepath.Join(stateDir, "vm-disks")
	if err := os.MkdirAll(disks, 0o700); err != nil {
		t.Fatal(err)
	}
	// New fingerprints the guest init, so both files have to be real.
	kernel, guestInit := filepath.Join(stateDir, "vmlinux"), filepath.Join(stateDir, "init")
	for _, f := range []string{kernel, guestInit} {
		if err := os.WriteFile(f, []byte("stub"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	swap := filepath.Join(disks, "swap-applevf-abc123.img")
	home := filepath.Join(disks, "019ff000-0000-7000-8000-000000000001.img")
	for _, path := range []string{swap, home} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := NewDriver(&fakeBackend{}, kernel, guestInit, stateDir); err != nil {
		t.Fatalf("NewDriver: %v", err)
	}

	if _, err := os.Stat(swap); !os.IsNotExist(err) {
		t.Errorf("swap disk survived: %v", err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Errorf("home disk was removed: %v", err)
	}
}

// A VM spends its whole boot between Create and answering. Reporting that as stopped made a machine
// look broken for the seconds it takes to come up.
func TestListReportsAStartingVM(t *testing.T) {
	d := newTestDriver(t)
	h, err := d.Create(context.Background(), sandbox.SandboxSpec{
		Image: "ghcr.io/x/agent:latest",
		Cmd:   []string{"sh", "-c", "true"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := stateOf(t, d, h); got != sandbox.InstanceStopped {
		t.Errorf("before Start: %q, want stopped", got)
	}

	// Set by hand: booting a real VM is what this test cannot do, and the flag is the whole point.
	d.mu.Lock()
	d.sandboxes[h].starting = true
	d.mu.Unlock()

	if got := stateOf(t, d, h); got != sandbox.InstanceStarting {
		t.Errorf("while starting: %q, want starting", got)
	}
}

func stateOf(t *testing.T, d *Driver, h sandbox.ID) sandbox.InstanceState {
	t.Helper()
	instances, err := d.ListSandboxes(context.Background())
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	for _, inst := range instances {
		if inst.ID == h {
			return inst.State
		}
	}
	t.Fatalf("sandbox %q not listed", h)
	return ""
}

// The console is named after the handle, and the handle already starts with "vm-". Composing the two
// once produced vm-vm-<id>.log, so this pins the name to a real handle rather than a literal.
func TestConsolePathNamesTheHandleOnce(t *testing.T) {
	d := &Driver{logs: t.TempDir()}
	handle := sandbox.ID(newHandleID())

	name := filepath.Base(d.consolePath(handle))

	if want := string(handle) + ".log"; name != want {
		t.Errorf("console file %q, want %q", name, want)
	}
	if got := strings.Count(name, "vm-"); got != 1 {
		t.Errorf("%q says vm- %d times, want once", name, got)
	}
}

func TestSweepOldConsoles(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		age     time.Duration
		removed bool
	}{
		{name: "written today", age: 0},
		{name: "written last week", age: 7 * 24 * time.Hour},
		{name: "untouched for two months", age: 60 * 24 * time.Hour, removed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "vm-abc123.log")
			if err := os.WriteFile(path, []byte("boot"), 0o600); err != nil {
				t.Fatal(err)
			}
			written := now.Add(-tc.age)
			if err := os.Chtimes(path, written, written); err != nil {
				t.Fatal(err)
			}

			d := &Driver{logs: dir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			d.sweepOldConsoleLogFiles(now)

			_, err := os.Stat(path)
			if gone := os.IsNotExist(err); gone != tc.removed {
				t.Errorf("removed = %v, want %v", gone, tc.removed)
			}
		})
	}
}

// The console file and the sweep that clears it are both named from the handle prefix, so a change to
// one cannot leave the other behind.
func TestConsoleSweepFollowsTheHandlePrefix(t *testing.T) {
	dir := t.TempDir()
	d := &Driver{logs: dir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	path := d.consolePath(sandbox.ID(newHandleID()))
	if err := os.WriteFile(path, []byte("boot"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	d.sweepOldConsoleLogFiles(time.Now())

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s survived the sweep: %v", path, err)
	}
}
