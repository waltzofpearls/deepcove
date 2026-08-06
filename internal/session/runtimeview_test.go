package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// listerSandbox is a fakeSandbox that can also enumerate itself, standing in for a driver that
// implements sandbox.Lister. The list it returns is set per test rather than derived from the fake's
// own state, because the whole point of the view is to show what happens when the driver and the
// session table DISAGREE.
type listerSandbox struct {
	*fakeSandbox
	instances []sandbox.Instance
	listErr   error
}

func (l *listerSandbox) ListSandboxes(context.Context) ([]sandbox.Instance, error) {
	if l.listErr != nil {
		return nil, l.listErr
	}
	return l.instances, nil
}

// hosts makes the driver report a sandbox that no session created, the shape of a leak. It registers
// the handle with the underlying fake too, so a later Destroy resolves it the way a real driver would.
func (l *listerSandbox) hosts(h sandbox.Handle) {
	l.instances = append(l.instances, sandbox.Instance{Handle: h, State: sandbox.InstanceRunning})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.created[h] = sandbox.SandboxSpec{}
}

// forget drops every sandbox the fake ever created, which is what a restart does to a VM runtime: the
// registry lived in the previous process, so a later Start must be told the handle is unknown.
func (l *listerSandbox) forget() {
	l.instances = nil
	l.mu.Lock()
	defer l.mu.Unlock()
	l.created = map[sandbox.Handle]sandbox.SandboxSpec{}
	l.started = map[sandbox.Handle]bool{}
}

// newListerService builds a service whose default runtime can enumerate its sandboxes.
func newListerService(t *testing.T) (*Service, *listerSandbox) {
	t.Helper()
	lister := &listerSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithRuntime(t, NewDuckDBSessionRepository(openTestDB(t)), lister)
	return svc, lister
}

// viewFor picks one runtime out of the report, so a test does not index into a slice whose order is an
// implementation detail.
func viewFor(t *testing.T, views []RuntimeView, name string) RuntimeView {
	t.Helper()
	for _, v := range views {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no view for runtime %q in %+v", name, views)
	return RuntimeView{}
}

func TestRuntimeViewsJoinsSandboxesToSessions(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	lister.instances = []sandbox.Instance{{
		Handle:    sandbox.Handle(sess.ContainerID),
		State:     sandbox.InstanceRunning,
		Image:     "alpine:3.20",
		Resources: sandbox.Resources{CPU: 2, Memory: 2 << 30},
		CreatedAt: time.Now(),
	}}

	views, err := svc.RuntimeViews(ctx)
	if err != nil {
		t.Fatalf("RuntimeViews: %v", err)
	}
	view := viewFor(t, views, RuntimeDocker)

	if !view.ListSupported {
		t.Error("ListSupported = false, want true for a driver implementing Lister")
	}
	if len(view.Sandboxes) != 1 {
		t.Fatalf("got %d sandboxes, want 1", len(view.Sandboxes))
	}
	sb := view.Sandboxes[0]
	if sb.SessionID != sess.ID || sb.SessionName != sess.Name {
		t.Errorf("sandbox joined to session %q/%q, want %q/%q", sb.SessionID, sb.SessionName, sess.ID, sess.Name)
	}
	if sb.Leaked() {
		t.Error("Leaked() = true for a sandbox its session still owns")
	}
	// Bytes come back as MiB, the units the API speaks.
	if sb.Resources.MemoryMB != 2048 || sb.Resources.CPU != 2 {
		t.Errorf("resources = %+v, want 2 CPU / 2048 MiB", sb.Resources)
	}
	if len(view.Orphaned) != 0 {
		t.Errorf("got %d orphaned sessions, want 0", len(view.Orphaned))
	}
}

// A sandbox with no session behind it is the leak this view exists to surface: it is still holding
// memory for work that is over, and for a VM runtime nothing else on the machine can show it.
func TestRuntimeViewsFlagsSandboxWithNoSession(t *testing.T) {
	svc, lister := newListerService(t)
	lister.hosts("left-behind")

	views, err := svc.RuntimeViews(context.Background())
	if err != nil {
		t.Fatalf("RuntimeViews: %v", err)
	}
	view := viewFor(t, views, RuntimeDocker)

	if len(view.Sandboxes) != 1 {
		t.Fatalf("got %d sandboxes, want 1", len(view.Sandboxes))
	}
	if !view.Sandboxes[0].Leaked() {
		t.Error("Leaked() = false for a sandbox no session owns")
	}
}

// The mirror image: the session survived but its sandbox did not, which is what every VM session looks
// like after a restart, since that driver's registry lives only in memory.
func TestRuntimeViewsFlagsSessionWithNoSandbox(t *testing.T) {
	svc, lister := newListerService(t)
	sess := mustCreate(t, svc, "claude")
	lister.instances = nil // the driver has forgotten it

	views, err := svc.RuntimeViews(context.Background())
	if err != nil {
		t.Fatalf("RuntimeViews: %v", err)
	}
	view := viewFor(t, views, RuntimeDocker)

	if len(view.Sandboxes) != 0 {
		t.Errorf("got %d sandboxes, want 0", len(view.Sandboxes))
	}
	if len(view.Orphaned) != 1 {
		t.Fatalf("got %d orphaned sessions, want 1", len(view.Orphaned))
	}
	if got := view.Orphaned[0]; got.SessionID != sess.ID || got.Handle != sess.ContainerID {
		t.Errorf("orphaned = %+v, want session %q handle %q", got, sess.ID, sess.ContainerID)
	}
}

// A stopped session still owns its sandbox. Listing only running sessions would report every stopped
// session's sandbox as leaked and invite deleting something still in use.
func TestRuntimeViewsCountsStoppedSessionsAsOwners(t *testing.T) {
	svc, lister := newListerService(t)
	ctx := context.Background()

	sess := mustCreate(t, svc, "claude")
	if err := svc.Stop(ctx, sess.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	lister.instances = []sandbox.Instance{{
		Handle: sandbox.Handle(sess.ContainerID),
		State:  sandbox.InstanceStopped,
	}}

	view := viewFor(t, mustViews(t, svc), RuntimeDocker)
	if len(view.Sandboxes) != 1 {
		t.Fatalf("got %d sandboxes, want 1", len(view.Sandboxes))
	}
	if view.Sandboxes[0].Leaked() {
		t.Error("a stopped session's sandbox was reported as leaked")
	}
}

// A driver that cannot enumerate is a capability gap, not a failure, and must not read as "hosting
// nothing".
func TestRuntimeViewsReportsListingUnsupported(t *testing.T) {
	svc, _, _, _ := newTestService(t) // plain fakeSandbox, no Lister

	view := viewFor(t, mustViews(t, svc), RuntimeDocker)
	if view.ListSupported {
		t.Error("ListSupported = true for a driver that does not implement Lister")
	}
	if view.ListError != "" {
		t.Errorf("ListError = %q, want empty: not listing is not an error", view.ListError)
	}
}

// A listing that fails must say so, or an empty list is misread as "nothing is running".
func TestRuntimeViewsReportsListingError(t *testing.T) {
	svc, lister := newListerService(t)
	lister.listErr = errors.New("daemon went away")

	view := viewFor(t, mustViews(t, svc), RuntimeDocker)
	if !view.ListSupported {
		t.Error("ListSupported = false, want true: the driver implements Lister, the call just failed")
	}
	if view.ListError == "" {
		t.Error("ListError is empty after a failed listing")
	}
}

func TestDestroyLeakedSandbox(t *testing.T) {
	ctx := context.Background()

	t.Run("destroys a sandbox no session owns", func(t *testing.T) {
		svc, lister := newListerService(t)
		lister.hosts("left-behind")

		if err := svc.DestroyLeakedSandbox(ctx, RuntimeDocker, "left-behind"); err != nil {
			t.Fatalf("DestroyLeakedSandbox: %v", err)
		}
		if !lister.destroyed["left-behind"] {
			t.Error("the driver was never asked to destroy the sandbox")
		}
	})

	t.Run("refuses a sandbox a session still owns", func(t *testing.T) {
		svc, lister := newListerService(t)
		sess := mustCreate(t, svc, "claude")
		lister.instances = []sandbox.Instance{{Handle: sandbox.Handle(sess.ContainerID)}}

		err := svc.DestroyLeakedSandbox(ctx, RuntimeDocker, sess.ContainerID)
		if !errors.Is(err, ErrSandboxHasSession) {
			t.Fatalf("error = %v, want ErrSandboxHasSession", err)
		}
		if lister.destroyed[sandbox.Handle(sess.ContainerID)] {
			t.Error("a sandbox belonging to a live session was destroyed")
		}
	})

	t.Run("reports a handle the runtime is not hosting", func(t *testing.T) {
		svc, lister := newListerService(t)
		lister.instances = nil

		if err := svc.DestroyLeakedSandbox(ctx, RuntimeDocker, "ghost"); !errors.Is(err, ErrSandboxNotFound) {
			t.Fatalf("error = %v, want ErrSandboxNotFound", err)
		}
	})

	t.Run("rejects a runtime this node does not provide", func(t *testing.T) {
		svc, _ := newListerService(t)

		var unknown ErrUnknownRuntime
		if err := svc.DestroyLeakedSandbox(ctx, "nonesuch", "x"); !errors.As(err, &unknown) {
			t.Fatalf("error = %v, want ErrUnknownRuntime", err)
		}
	})
}

func TestFromSandboxResources(t *testing.T) {
	cases := []struct {
		name string
		in   sandbox.Resources
		want Resources
	}{
		{"zero stays zero", sandbox.Resources{}, Resources{}},
		{"bytes become MiB", sandbox.Resources{CPU: 4, Memory: 2 << 30, Disk: 512 << 20}, Resources{CPU: 4, MemoryMB: 2048, DiskMB: 512}},
		{"a sub-MiB value rounds down to zero, which reads as no limit", sandbox.Resources{Memory: 1024}, Resources{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fromSandboxResources(tc.in); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func mustViews(t *testing.T, svc *Service) []RuntimeView {
	t.Helper()
	views, err := svc.RuntimeViews(context.Background())
	if err != nil {
		t.Fatalf("RuntimeViews: %v", err)
	}
	return views
}
