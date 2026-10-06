package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// newBareService is a service with no runtime registered, which is how the CLI starts before it
// registers any.
func newBareService(t *testing.T) *Service {
	t.Helper()
	database := db.OpenForTest(t)
	svc := NewService(NewDuckDBSessionRepository(database), nil)
	svc.SetWorkspaceVMStore(NewDuckDBWorkspaceVMStore(database))
	return svc
}

// A fresh service provides nothing. Runtimes come from whoever builds it, so no runtime is privileged
// by construction and adding one never means editing the service. A node with none still starts, so
// its UI can set one up.
func TestNewServiceStartsWithNoRuntimes(t *testing.T) {
	svc := newBareService(t)

	if got := svc.Runtimes(); len(got) != 0 {
		t.Errorf("Runtimes() = %v, want none before anything registers", got)
	}
	if got := svc.DefaultRuntime(); got != "" {
		t.Errorf("DefaultRuntime() = %q, want empty until one is selected", got)
	}
	if err := svc.SelectDefaultRuntime(context.Background(), ""); err != nil {
		t.Errorf("SelectDefaultRuntime: %v, want nil with no runtimes registered", err)
	}
	if got := svc.DefaultRuntime(); got != "" {
		t.Errorf("DefaultRuntime() = %q, want empty with nothing to pick", got)
	}
}

func TestSelectDefaultRuntime(t *testing.T) {
	newSvc := func(t *testing.T, names ...string) *Service {
		svc := newBareService(t)
		for _, n := range names {
			svc.RegisterRuntime(n, newFakeSandbox())
		}
		return svc
	}

	t.Run("no preference takes the strongest isolation available", func(t *testing.T) {
		svc := newSvc(t, RuntimeSandboxless, RuntimeAppleVF)
		if err := svc.SelectDefaultRuntime(context.Background(), ""); err != nil {
			t.Fatalf("SelectDefaultRuntime: %v", err)
		}
		if got := svc.DefaultRuntime(); got != RuntimeAppleVF {
			t.Errorf("default = %q, want %q (runtimeOrder puts the VM first)", got, RuntimeAppleVF)
		}
	})

	t.Run("falls to what the node actually has", func(t *testing.T) {
		svc := newSvc(t, RuntimeCloudHV)
		if err := svc.SelectDefaultRuntime(context.Background(), ""); err != nil {
			t.Fatalf("SelectDefaultRuntime: %v", err)
		}
		if got := svc.DefaultRuntime(); got != RuntimeCloudHV {
			t.Errorf("default = %q, want %q", got, RuntimeCloudHV)
		}
	})

	t.Run("no sandbox is never picked for you", func(t *testing.T) {
		svc := newSvc(t, RuntimeSandboxless)
		if err := svc.SelectDefaultRuntime(context.Background(), ""); err != nil {
			t.Fatalf("SelectDefaultRuntime: %v", err)
		}
		if got := svc.DefaultRuntime(); got != "" {
			t.Errorf("default = %q, want none: running unisolated has to be asked for", got)
		}
	})

	t.Run("an explicit preference wins", func(t *testing.T) {
		svc := newSvc(t, RuntimeSandboxless, RuntimeAppleVF)
		if err := svc.SelectDefaultRuntime(context.Background(), RuntimeSandboxless); err != nil {
			t.Fatalf("SelectDefaultRuntime: %v", err)
		}
		if got := svc.DefaultRuntime(); got != RuntimeSandboxless {
			t.Errorf("default = %q, want the requested %q", got, RuntimeSandboxless)
		}
	})

	t.Run("a preference this node cannot provide is reported, and the node still gets a default", func(t *testing.T) {
		svc := newSvc(t, RuntimeSandboxless, RuntimeCloudHV)
		var unknown ErrUnknownRuntime
		if err := svc.SelectDefaultRuntime(context.Background(), RuntimeAppleVF); !errors.As(err, &unknown) {
			t.Fatalf("error = %v, want ErrUnknownRuntime", err)
		}
		if got := svc.DefaultRuntime(); got != RuntimeCloudHV {
			t.Errorf("default = %q, want the fallback %q", got, RuntimeCloudHV)
		}
	})
}

func TestRegisterRuntimeAddsToTheList(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.RegisterRuntime(RuntimeAppleVF, newFakeSandbox())

	got := svc.Runtimes()
	for _, want := range []string{runtimeFake, RuntimeAppleVF} {
		if !slices.Contains(got, want) {
			t.Errorf("Runtimes() = %v, must contain %q", got, want)
		}
	}
	// A nil driver must not register: it would panic on first use rather than fail cleanly.
	svc.RegisterRuntime("bogus", nil)
	if slices.Contains(svc.Runtimes(), "bogus") {
		t.Error("a nil driver must not be registered")
	}
}

// A session records its runtime at create time, so later operations can route back to the same driver.
func TestCreateRecordsTheRuntime(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Runtime != runtimeFake {
		t.Errorf("Runtime = %q, want the default %q", sess.Runtime, runtimeFake)
	}
}

func TestCreateHonoursTheRequestedRuntime(t *testing.T) {
	svc, defaultFake, _, _ := newTestService(t)
	vmFake := newFakeSandbox()
	svc.RegisterRuntime(RuntimeAppleVF, vmFake)

	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Runtime: RuntimeAppleVF})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.Runtime != RuntimeAppleVF {
		t.Errorf("Runtime = %q, want %q", sess.Runtime, RuntimeAppleVF)
	}
	// The session must have been built by the requested driver, not the default one.
	if vmFake.liveCount() != 1 {
		t.Errorf("the requested runtime should host the session; live = %d", vmFake.liveCount())
	}
	if defaultFake.liveCount() != 0 {
		t.Errorf("the default runtime should not have been used; live = %d", defaultFake.liveCount())
	}
}

// Asking for a runtime this node does not provide must fail before anything is created, rather than
// leaving a half built session behind.
func TestCreateRejectsAnUnavailableRuntime(t *testing.T) {
	svc, fake, _, db := newTestService(t)

	_, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Runtime: RuntimeAppleVF})
	var unknown ErrUnknownRuntime
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want ErrUnknownRuntime", err)
	}
	if unknown.Runtime != RuntimeAppleVF {
		t.Errorf("the error should name the runtime, got %q", unknown.Runtime)
	}
	if fake.liveCount() != 0 {
		t.Error("nothing should be created when the runtime is unavailable")
	}
	if rowCount(t, db) != 0 {
		t.Error("no session row should be written when the runtime is unavailable")
	}
}

// Operations on a session route back to the driver that created it, not to the default.
func TestOperationsRouteToTheSessionsRuntime(t *testing.T) {
	svc, defaultFake, _, _ := newTestService(t)
	vmFake := newFakeSandbox()
	svc.RegisterRuntime(RuntimeAppleVF, vmFake)

	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Runtime: RuntimeAppleVF})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Stop(context.Background(), sess.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if !strings.Contains(vmFake.callSequence(), "Stop") {
		t.Errorf("Stop should reach the session's runtime; calls = %q", vmFake.callSequence())
	}
	if strings.Contains(defaultFake.callSequence(), "Stop") {
		t.Errorf("Stop must not reach the default runtime; calls = %q", defaultFake.callSequence())
	}
}

// A session whose runtime is no longer available (created on another node, or the runtime went away)
// must report that clearly rather than fall back to a driver that knows nothing about it.
func TestOperationsRejectAnUnknownRuntime(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	vmFake := newFakeSandbox()
	svc.RegisterRuntime(RuntimeAppleVF, vmFake)

	sess, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Runtime: RuntimeAppleVF})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The runtime disappears, as it would on a node that cannot provide it.
	delete(svc.runtimes, RuntimeAppleVF)

	var unknown ErrUnknownRuntime
	if err := svc.Stop(context.Background(), sess.ID); !errors.As(err, &unknown) {
		t.Errorf("Stop error = %v, want ErrUnknownRuntime", err)
	}
}

// An empty runtime is a request that named none, and resolves to the default.
func TestEmptyRuntimeFallsBackToTheDefault(t *testing.T) {
	svc, _, _, _ := newTestService(t)

	if _, err := svc.runtimeFor(&Session{Runtime: ""}); err != nil {
		t.Errorf("an empty runtime should resolve to the default, got %v", err)
	}
}

func TestToSandboxResources(t *testing.T) {
	cases := []struct {
		name       string
		in         Resources
		wantCPU    float64
		wantMemory int64
		wantDisk   int64
	}{
		{"zero stays zero so drivers apply their own defaults", Resources{}, 0, 0, 0},
		{"MiB become bytes", Resources{CPU: 4, MemoryMB: 2048, DiskMB: 512}, 4, 2048 << 20, 512 << 20},
		{"negatives are ignored rather than trusted", Resources{MemoryMB: -1, DiskMB: -1}, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toSandboxResources(tc.in)
			if got.CPU != tc.wantCPU || got.Memory != tc.wantMemory || got.Disk != tc.wantDisk {
				t.Errorf("got cpu=%v mem=%d disk=%d, want cpu=%v mem=%d disk=%d",
					got.CPU, got.Memory, got.Disk, tc.wantCPU, tc.wantMemory, tc.wantDisk)
			}
		})
	}
}

// Resources chosen at create time have to survive a round trip, or a recreate would silently resize the
// session.
func TestCreatePersistsResources(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	want := Resources{CPU: 4, MemoryMB: 4096, DiskMB: 1024}

	created, err := svc.Create(context.Background(), CreateRequest{Agent: "claude", Resources: want})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Resources != want {
		t.Errorf("returned resources = %+v, want %+v", created.Resources, want)
	}

	reloaded, err := svc.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reloaded.Resources != want {
		t.Errorf("reloaded resources = %+v, want %+v", reloaded.Resources, want)
	}
}

// Validation is server side too, because the CLI and the API bypass the form.
func TestResourcesValidate(t *testing.T) {
	cases := []struct {
		name    string
		in      Resources
		wantErr bool
	}{
		{"zero means no limit and always passes", Resources{}, false},
		{"a plausible request passes", Resources{CPU: 8, MemoryMB: 16384, DiskMB: 4096}, false},
		{"negative cpu is rejected", Resources{CPU: -1}, true},
		{"negative memory is rejected", Resources{MemoryMB: -1}, true},
		{"negative disk is rejected", Resources{DiskMB: -1}, true},
		{"an absurd cpu count is rejected", Resources{CPU: 1000}, true},
		{"an absurd memory request is rejected", Resources{MemoryMB: 1 << 30}, true},
		{"an absurd disk request is rejected", Resources{DiskMB: 1 << 30}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.in.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// Map iteration is random, so without a deliberate order the create picker reshuffles between restarts
// and there is no stable first option.
func TestRuntimesOrder(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.RegisterRuntime(RuntimeAppleVF, newFakeSandbox())
	svc.RegisterRuntime(RuntimeSandboxless, newFakeSandbox())
	svc.RegisterRuntime("zebra", newFakeSandbox())

	got := svc.Runtimes()
	want := []string{RuntimeAppleVF, RuntimeSandboxless, runtimeFake, "zebra"}
	if !slices.Equal(got, want) {
		t.Errorf("Runtimes() = %v, want %v (VM first, then sandboxless, then anything unknown)", got, want)
	}

	// Stable across calls, not just ordered once.
	if second := svc.Runtimes(); !slices.Equal(got, second) {
		t.Errorf("Runtimes() returned %v then %v; the order must not move", got, second)
	}
}

// unreachableSandbox is a driver whose backend is down: registered, but unable to host anything.
type unreachableSandbox struct{ *fakeSandbox }

func (unreachableSandbox) Ping(context.Context) (string, error) {
	return "", fmt.Errorf("dial: no such file or directory")
}

// Registering a driver says this node knows how to talk to that backend, not that the backend is up.
// Creating against a down one must be refused before any work, and in the node's own words.
func TestCreateRefusesAnUnavailableRuntime(t *testing.T) {
	svc := newBareService(t)
	down := unreachableSandbox{fakeSandbox: newFakeSandbox()}
	svc.RegisterRuntime(runtimeFake, down)
	svc.setDefaultRuntime(runtimeFake)

	_, err := svc.Create(context.Background(), CreateRequest{Agent: "claude"})

	var unavailable ErrRuntimeUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want ErrRuntimeUnavailable", err)
	}
	if unavailable.Runtime != runtimeFake {
		t.Errorf("runtime = %q, want %q", unavailable.Runtime, runtimeFake)
	}
	// Refused before any lifecycle call, so there is no half built sandbox to clean up.
	if seq := down.callSequence(); seq != "" {
		t.Errorf("driver lifecycle calls = %q, want none: the runtime was known to be down", seq)
	}
}

// The default must not land on a runtime that cannot work when a working one is registered.
func TestSelectDefaultRuntimeSkipsUnavailable(t *testing.T) {
	svc := newBareService(t)
	// applevf sorts first in runtimeOrder, so a naive pick would choose it even though it is down.
	svc.RegisterRuntime(RuntimeAppleVF, unreachableSandbox{fakeSandbox: newFakeSandbox()})
	svc.RegisterRuntime(RuntimeCloudHV, newFakeSandbox())

	if err := svc.SelectDefaultRuntime(context.Background(), ""); err != nil {
		t.Fatalf("SelectDefaultRuntime: %v", err)
	}
	if got := svc.DefaultRuntime(); got != RuntimeCloudHV {
		t.Errorf("default = %q, want %q: the preferred runtime is unreachable", got, RuntimeCloudHV)
	}
}

// A VM that missed its check stays the default over No sandbox, which would otherwise be the default
// until the next restart. Creating on the VM is what gets refused, with the reason.
func TestSelectDefaultRuntime_PrefersAnUnreachableVMOverNoSandbox(t *testing.T) {
	svc := newBareService(t)
	svc.RegisterRuntime(RuntimeAppleVF, unreachableSandbox{fakeSandbox: newFakeSandbox()})
	svc.RegisterRuntime(RuntimeSandboxless, newFakeSandbox())

	if err := svc.SelectDefaultRuntime(context.Background(), ""); err != nil {
		t.Fatalf("SelectDefaultRuntime: %v", err)
	}
	if got := svc.DefaultRuntime(); got != RuntimeAppleVF {
		t.Errorf("default = %q, want %q: a VM that missed one check still beats no isolation", got, RuntimeAppleVF)
	}
}

func TestCreate_AsksForARuntimeWhenOnlyNoSandboxIsOffered(t *testing.T) {
	svc := newBareService(t)
	svc.RegisterRuntime(RuntimeSandboxless, newFakeSandbox())
	if err := svc.SelectDefaultRuntime(context.Background(), ""); err != nil {
		t.Fatalf("SelectDefaultRuntime: %v", err)
	}

	_, err := svc.Create(context.Background(), CreateRequest{Agent: "claude"})

	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "no VM runtime") {
		t.Errorf("error = %v, want a pointer to setting up a VM runtime", err)
	}
}

func TestPickVMRuntime(t *testing.T) {
	cases := []struct {
		name           string
		runtimes       []string
		defaultRuntime string
		requested      string
		want           string
		wantErr        bool
	}{
		{name: "the runtime asked for", runtimes: []string{RuntimeAppleVF}, defaultRuntime: RuntimeAppleVF,
			requested: RuntimeAppleVF, want: RuntimeAppleVF},
		{name: "a runtime with no vm asked for is refused", runtimes: []string{RuntimeSandboxless},
			defaultRuntime: RuntimeSandboxless, requested: RuntimeSandboxless, wantErr: true},
		{name: "the node default when it boots vms", runtimes: []string{RuntimeAppleVF, RuntimeSandboxless},
			defaultRuntime: RuntimeAppleVF, want: RuntimeAppleVF},
		{name: "the vm runtime even when the default has no vm",
			runtimes: []string{RuntimeAppleVF, RuntimeSandboxless}, defaultRuntime: RuntimeSandboxless, want: RuntimeAppleVF},
		{name: "a node with no vm runtime", runtimes: []string{RuntimeSandboxless}, defaultRuntime: RuntimeSandboxless,
			wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{runtimes: map[string]sandbox.Sandbox{}, defaultRuntime: tc.defaultRuntime}
			for _, name := range tc.runtimes {
				svc.runtimes[name] = newFakeSandbox()
			}
			got, err := svc.pickVMRuntime(tc.requested)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidInput) {
					t.Fatalf("got %v, want errors.Is(err, ErrInvalidInput)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("pickVMRuntime: %v", err)
			}
			if got != tc.want {
				t.Errorf("runtime = %q, want %q", got, tc.want)
			}
		})
	}
}

// A runtime is installed from an HTTP request while other requests are reading the same map, which used
// to be a "concurrent map read and map write" crash. Run with -race.
func TestRegisterRuntimeWhileReading(t *testing.T) {
	svc, driver, _, _ := newTestService(t)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.RegisterRuntime(fmt.Sprintf("runtime-%d", i), driver)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.Runtimes()
			_ = svc.DefaultRuntime()
			_, _ = svc.runtimeByName("")
			_, _ = svc.pickVMRuntime("")
			rec := httptest.NewRecorder()
			svc.handlePreflight(rec, httptest.NewRequest(http.MethodGet, "/api/preflight", nil))
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.SelectDefaultRuntime(context.Background(), "")
		}()
	}
	wg.Wait()
}

func TestSandboxesOutliveDeepcove(t *testing.T) {
	cases := []struct {
		runtime string
		want    bool
	}{
		{runtime: RuntimeSandboxless, want: true},
		{runtime: RuntimeAppleVF},
		{runtime: RuntimeCloudHV},
		{runtime: "something we have never heard of"},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			if got := SandboxesOutliveDeepcove(tc.runtime); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRuntimeByName(t *testing.T) {
	cases := []struct {
		name           string
		runtimes       []string
		defaultRuntime string
		ask            string
		want           string
		wantErr        error
	}{
		{name: "a runtime by its name", runtimes: []string{RuntimeCloudHV}, ask: RuntimeCloudHV, want: RuntimeCloudHV},
		{name: "no name takes the default", runtimes: []string{RuntimeCloudHV}, defaultRuntime: RuntimeCloudHV, want: RuntimeCloudHV},
		{name: "no name and no default asks for one", runtimes: []string{RuntimeSandboxless}, wantErr: ErrInvalidInput},
		{name: "no name on a node with nothing", wantErr: ErrUnknownRuntime{}},
		{name: "a name this node lacks", runtimes: []string{RuntimeCloudHV}, ask: RuntimeAppleVF, wantErr: ErrUnknownRuntime{Runtime: RuntimeAppleVF}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newBareService(t)
			drivers := map[string]sandbox.Sandbox{}
			for _, name := range tc.runtimes {
				drivers[name] = newFakeSandbox()
				svc.RegisterRuntime(name, drivers[name])
			}
			svc.setDefaultRuntime(tc.defaultRuntime)

			got, err := svc.runtimeByName(tc.ask)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != drivers[tc.want] {
				t.Errorf("got %v, %v; want the %s driver", got, err, tc.want)
			}
		})
	}
}

func TestResolveRuntimeName(t *testing.T) {
	cases := []struct {
		name           string
		runtimes       []string
		defaultRuntime string
		ask            string
		want           string
		wantErr        error
	}{
		{name: "a named runtime is kept", ask: RuntimeCloudHV, want: RuntimeCloudHV},
		{name: "no name takes the default", runtimes: []string{RuntimeCloudHV}, defaultRuntime: RuntimeCloudHV, want: RuntimeCloudHV},
		{name: "no name and no default asks for one", runtimes: []string{RuntimeSandboxless}, wantErr: ErrInvalidInput},
		{name: "no name on a node with nothing", wantErr: ErrUnknownRuntime{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newBareService(t)
			for _, name := range tc.runtimes {
				svc.RegisterRuntime(name, newFakeSandbox())
			}
			svc.setDefaultRuntime(tc.defaultRuntime)

			got, err := svc.resolveRuntimeName(tc.ask)

			if !errors.Is(err, tc.wantErr) || got != tc.want {
				t.Errorf("got %q, %v; want %q, %v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestVMSetupAdvice(t *testing.T) {
	cases := []struct {
		name        string
		missing     string
		installable bool
		want        string
	}{
		{name: "setup can fetch it", missing: MissingKernel, installable: true, want: "Set up deepcove"},
		{name: "no KVM", missing: MissingKVM, want: "KVM is missing"},
		{name: "failed to start, though setup ran", missing: MissingStartFailed, installable: true, want: "failed to start"},
		{name: "no guest init", missing: MissingGuestInit, want: "reinstall deepcove"},
		{name: "missing, with nothing to install it", missing: MissingKernel, want: "service status"},
		{name: "no VM runtime on this OS", want: "no virtual machine runtime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newBareService(t)
			if tc.missing != "" {
				svc.MarkRuntimeMissing(RuntimeCloudHV, tc.missing)
			}
			if tc.installable {
				svc.SetRuntimeInstaller(RuntimeCloudHV, func(context.Context, func(sandbox.PullProgress)) error { return nil })
			}
			if got := svc.vmSetupAdvice(); !strings.Contains(got, tc.want) {
				t.Errorf("got %q, want it to mention %q", got, tc.want)
			}
		})
	}
}
