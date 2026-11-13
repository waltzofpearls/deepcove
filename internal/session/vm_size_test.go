package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"slices"
	"testing"

	"github.com/waltzofpearls/deepcove"
	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestSessionVMsThatFit(t *testing.T) {
	const macKeptBackMB, linuxKeptBackMB = 4 << 10, 1 << 10
	cases := []struct {
		name         string
		size         string
		hostMemoryMB int64
		keptBackMB   int64
		want         int
	}{
		{"16 GB Mac, small", VMSizeSmall, 16 << 10, macKeptBackMB, 12},
		{"16 GB Mac, medium", VMSizeMedium, 16 << 10, macKeptBackMB, 6},
		{"32 GB Mac, medium", VMSizeMedium, 32 << 10, macKeptBackMB, 14},
		{"4 GB Linux server, small", VMSizeSmall, 3915, linuxKeptBackMB, 2},
		{"4 GB Linux server, medium", VMSizeMedium, 3915, linuxKeptBackMB, 1},
		{"a host smaller than what it keeps back", VMSizeSmall, 2 << 10, macKeptBackMB, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			size, _ := findVMSize(SessionVMSizes, tc.size)
			if got := sessionVMsThatFit(size, tc.hostMemoryMB, tc.keptBackMB); got != tc.want {
				t.Errorf("sessionVMsThatFit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestWorkspaceVMSizes(t *testing.T) {
	const macKeptBackMB, linuxKeptBackMB = 4 << 10, 1 << 10
	cases := []struct {
		name         string
		hostMemoryMB int64
		keptBackMB   int64
		want         []VMSize
	}{
		{"memory unknown", 0, linuxKeptBackMB, []VMSize{
			{Name: VMSizeSmall, CPU: 2, MemoryMB: 4 << 10},
			{Name: VMSizeMedium, CPU: 4, MemoryMB: 8 << 10},
		}},
		{"512 MB Linux", 512, linuxKeptBackMB, nil},
		{"2 GB Linux", 1946, linuxKeptBackMB, []VMSize{{Name: VMSizeSmall, CPU: 1, MemoryMB: 1 << 10}}},
		{"2.5 GB Linux, no scaled size big enough", 2560, linuxKeptBackMB, []VMSize{
			{Name: VMSizeSmall, CPU: 1, MemoryMB: 1 << 10},
		}},
		{"4 GB Linux", 3915, linuxKeptBackMB, []VMSize{{Name: VMSizeSmall, CPU: 1, MemoryMB: 2 << 10}}},
		{"16 GB Mac", 16 << 10, macKeptBackMB, []VMSize{
			{Name: VMSizeSmall, CPU: 1, MemoryMB: 3 << 10},
			{Name: VMSizeMedium, CPU: 3, MemoryMB: 6 << 10},
		}},
		{"32 GB Mac, capped", 32 << 10, macKeptBackMB, []VMSize{
			{Name: VMSizeSmall, CPU: 2, MemoryMB: 4 << 10},
			{Name: VMSizeMedium, CPU: 4, MemoryMB: 8 << 10},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workspaceVMSizes(tc.hostMemoryMB, tc.keptBackMB); !slices.Equal(got, tc.want) {
				t.Errorf("workspaceVMSizes = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDefaultWorkspaceVMSize(t *testing.T) {
	cases := []struct {
		name  string
		sizes []VMSize
		want  string
	}{
		{"has the usual default", workspaceVMSizes(32<<10, 4<<10), VMSizeMedium},
		{"lacks it, takes the biggest", workspaceVMSizes(3915, 1<<10), VMSizeSmall},
		{"only one size", workspaceVMSizes(1946, 1<<10), VMSizeSmall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultWorkspaceVMSize(tc.sizes); got != tc.want {
				t.Errorf("defaultWorkspaceVMSize = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentsThatFit(t *testing.T) {
	cases := []struct {
		memoryMB int64
		want     int
	}{
		{1 << 10, 1},
		{2 << 10, 2},
		{16 << 10, 16},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.memoryMB), func(t *testing.T) {
			if got := agentsThatFit(VMSize{MemoryMB: tc.memoryMB}); got != tc.want {
				t.Errorf("agentsThatFit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSessionVMSizeChoices_UnknownHostMemoryHasNoCount(t *testing.T) {
	for _, choice := range sessionVMSizeChoices(0) {
		if choice.FitsOnThisHost != nil {
			t.Errorf("%s: fits = %d, want no count when the host memory is unknown", choice.Name, *choice.FitsOnThisHost)
		}
	}
}

func TestVMLimits(t *testing.T) {
	cases := []struct {
		name         string
		cpus         int
		hostMemoryMB int64
		keptBackMB   int64
		want         VMLimits
	}{
		{"16 GB Mac", 8, 16 << 10, 4 << 10, VMLimits{MaxCPU: 8, MaxMemoryMB: 12 << 10}},
		{"memory unknown", 8, 0, 1 << 10, VMLimits{MaxCPU: 8}},
		{"a host smaller than what it keeps back", 8, 2 << 10, 4 << 10, VMLimits{MaxCPU: 8, MaxMemoryMB: 1 << 10}},
		{"more than a request may ask for", 128, 1 << 20, 4 << 10, VMLimits{MaxCPU: maxCPUCores, MaxMemoryMB: maxMB}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vmLimits(tc.cpus, tc.hostMemoryMB, tc.keptBackMB); got != tc.want {
				t.Errorf("vmLimits = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSpareMemoryMB(t *testing.T) {
	cases := []struct {
		name         string
		hostMemoryMB int64
		keptBackMB   int64
		isMeasured   bool
		wantMB       int64
	}{
		{"16 GB Mac", 16 << 10, 4 << 10, true, 12 << 10},
		{"memory unknown", 0, 4 << 10, false, 0},
		{"less than 1 GB left", 4<<10 + 512, 4 << 10, true, 512},
		{"a host smaller than what it keeps back", 2 << 10, 4 << 10, true, 0},
		{"more than a request may ask for", 1 << 20, 4 << 10, true, 1<<20 - 4<<10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := spareMemoryMB(tc.hostMemoryMB, tc.keptBackMB)
			if got == nil {
				if tc.isMeasured {
					t.Errorf("spareMemoryMB = nil, want %d", tc.wantMB)
				}
				return
			}
			if !tc.isMeasured || *got != tc.wantMB {
				t.Errorf("spareMemoryMB = %d, want %d (measured %v)", *got, tc.wantMB, tc.isMeasured)
			}
		})
	}
}

func TestValidateVMSize(t *testing.T) {
	limits := VMLimits{MaxCPU: 4, MaxMemoryMB: 12 << 10}
	cases := []struct {
		name      string
		resources Resources
		limits    VMLimits
		wantErr   bool
	}{
		{"no size", Resources{}, limits, false},
		{"a known size", Resources{Size: VMSizeSmall}, limits, false},
		{"an unknown size", Resources{Size: "huge"}, limits, true},
		{"a size and numbers", Resources{Size: VMSizeSmall, CPU: 4}, limits, true},
		{"a custom size", Resources{CPU: 4, MemoryMB: 12 << 10}, limits, false},
		{"cpu alone", Resources{CPU: 2}, limits, true},
		{"memory alone", Resources{MemoryMB: 2 << 10}, limits, true},
		{"disk alone", Resources{DiskMB: 8 << 10}, limits, true},
		{"less than the smallest size", Resources{CPU: 1, MemoryMB: 512}, limits, true},
		{"more cpu than the node has", Resources{CPU: 5, MemoryMB: 2 << 10}, limits, true},
		{"more memory than the node can spare", Resources{CPU: 2, MemoryMB: 13 << 10}, limits, true},
		{"memory unknown is not capped", Resources{CPU: 2, MemoryMB: 64 << 10}, VMLimits{MaxCPU: 4}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVMSize(tc.resources, SessionVMSizes, tc.limits)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput so the API answers 400", err)
			}
		})
	}
}

func TestResources_WithSizeApplied(t *testing.T) {
	cases := []struct {
		name      string
		resources Resources
		want      Resources
	}{
		{"small", Resources{Size: VMSizeSmall}, Resources{Size: VMSizeSmall, CPU: 1, MemoryMB: 1 << 10}},
		{"medium", Resources{Size: VMSizeMedium}, Resources{Size: VMSizeMedium, CPU: 1, MemoryMB: 2 << 10}},
		{"numbers and no size stay as they are", Resources{CPU: 3}, Resources{CPU: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.resources.withSizeApplied(SessionVMSizes); got != tc.want {
				t.Errorf("withSizeApplied = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestService_Create_AVMSessionGetsTheDefaultSize(t *testing.T) {
	svc, fake, _, _ := newTestService(t)
	sess := mustCreate(t, svc, "claude")

	if sess.Resources.Size != DefaultVMSize {
		t.Errorf("stored size = %q, want %q", sess.Resources.Size, DefaultVMSize)
	}
	medium, _ := findVMSize(SessionVMSizes, VMSizeMedium)
	booted := fake.specs[len(fake.specs)-1].Resources
	if booted.CPU != float64(medium.CPU) || booted.Memory != medium.MemoryMB<<20 {
		t.Errorf("booted with %+v, want medium's %d CPU and %d MB", booted, medium.CPU, medium.MemoryMB)
	}
}

func TestService_SaveWorkspaceVM_Size(t *testing.T) {
	cases := []struct {
		name    string
		asked   Resources
		want    Resources
		wantErr error
	}{
		{"no size gets the default", Resources{}, Resources{Size: DefaultVMSize}, nil},
		{"a picked size is kept", Resources{Size: VMSizeSmall}, Resources{Size: VMSizeSmall}, nil},
		{"a custom size is kept", Resources{CPU: 1, MemoryMB: 1 << 10}, Resources{CPU: 1, MemoryMB: 1 << 10}, nil},
		{"an unknown size is refused", Resources{Size: "huge"}, Resources{}, ErrInvalidInput},
		{"more cpu than the node has is refused", Resources{CPU: float64(runtime.NumCPU() + 1), MemoryMB: 1 << 10}, Resources{}, ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lister := &listerSandbox{fakeSandbox: newFakeSandbox()}
			svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), lister, RuntimeAppleVF)
			vm, err := svc.SaveWorkspaceVM(context.Background(), WorkspaceVMRequest{HostRoot: t.TempDir(), Resources: tc.asked})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SaveWorkspaceVM err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && vm.Resources != tc.want {
				t.Errorf("resources = %+v, want %+v", vm.Resources, tc.want)
			}
		})
	}
}

func TestService_SaveWorkspaceVM_KeepsItsSavedSize(t *testing.T) {
	cases := []struct {
		name    string
		saved   Resources
		asked   Resources
		wantErr error
	}{
		{"cpu alone, as an older version took it", Resources{CPU: 2}, Resources{CPU: 2}, nil},
		{"less memory than a new size may have", Resources{CPU: 1, MemoryMB: 512}, Resources{CPU: 1, MemoryMB: 512}, nil},
		{"a new size is still checked", Resources{Size: VMSizeSmall}, Resources{CPU: 2}, ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := workspaceService(t)
			ctx := context.Background()
			vm, err := svc.WorkspaceVM(ctx)
			if err != nil {
				t.Fatalf("WorkspaceVM: %v", err)
			}
			vm.Resources = tc.saved
			if err := svc.workspaceVM.Save(ctx, vm); err != nil {
				t.Fatalf("Save: %v", err)
			}

			_, err = svc.SaveWorkspaceVM(ctx, WorkspaceVMRequest{HostRoot: vm.HostRoot, Resources: tc.asked})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("SaveWorkspaceVM err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestWorkspaceVMSpec_RefusesAMachineWithNoSize(t *testing.T) {
	agent, _ := resolveAgent(defaultAgent)
	vm := &WorkspaceVM{HostRoot: t.TempDir(), GuestRoot: "/workspace", Resources: Resources{Size: VMSizeMedium}}
	if _, err := workspaceVMSpec(vm, agent, workspaceVMSizes(512, 1<<10)); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("error = %v, want a machine too small for a workspace VM refused", err)
	}
}

func TestWorkspaceVMSpec_BootsWithTheNamedSize(t *testing.T) {
	agent, _ := resolveAgent(defaultAgent)
	cases := []struct {
		name      string
		hostSizes []VMSize
		saved     string
		want      string
	}{
		{"a size this host has", workspaceVMSizes(32<<10, 4<<10), VMSizeSmall, VMSizeSmall},
		{"a size only a bigger host has boots as the default", workspaceVMSizes(3915, 1<<10), VMSizeMedium, VMSizeSmall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := &WorkspaceVM{HostRoot: t.TempDir(), GuestRoot: "/workspace", Resources: Resources{Size: tc.saved}}
			spec, err := workspaceVMSpec(vm, agent, tc.hostSizes)
			if err != nil {
				t.Fatalf("workspaceVMSpec: %v", err)
			}
			want, _ := findVMSize(tc.hostSizes, tc.want)
			if spec.Resources.Memory != want.MemoryMB<<20 || spec.Resources.CPU != float64(want.CPU) {
				t.Errorf("booted with %+v, want %s's %d CPU and %d MB", spec.Resources, tc.want, want.CPU, want.MemoryMB)
			}
		})
	}
}

func TestLargeWorkspaceVMResources(t *testing.T) {
	cases := []struct {
		name         string
		cpus         int
		hostMemoryMB int64
		keptBackMB   int64
		want         Resources
	}{
		{"memory unknown", 10, 0, 1 << 10, Resources{CPU: 8, MemoryMB: 16 << 10}},
		{"32 GB Mac, capped", 10, 32 << 10, 4 << 10, Resources{CPU: 8, MemoryMB: 16 << 10}},
		{"16 GB Mac", 8, 16 << 10, 4 << 10, Resources{CPU: 6, MemoryMB: 12 << 10}},
		{"16 GB Linux with 2 CPUs", 2, 16 << 10, 1 << 10, Resources{CPU: 2, MemoryMB: 15 << 10}},
		{"4 GB Linux", 2, 3915, 1 << 10, Resources{CPU: 1, MemoryMB: 2 << 10}},
		{"2 GB Linux had no Large", 1, 1946, 1 << 10, Resources{Size: VMSizeSmall}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := largeWorkspaceVMResources(tc.cpus, tc.hostMemoryMB, tc.keptBackMB); got != tc.want {
				t.Errorf("largeWorkspaceVMResources = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestService_ConvertLargeWorkspaceVM(t *testing.T) {
	cases := []struct {
		name  string
		saved Resources
		want  Resources
	}{
		{"large keeps its numbers", Resources{Size: legacyVMSizeLarge}, largeWorkspaceVMResources(hostVMLimits().MaxCPU, hostMemoryMB(), hostMemoryKeptBackMB)},
		{"a size still offered stays", Resources{Size: VMSizeSmall}, Resources{Size: VMSizeSmall}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := workspaceService(t)
			ctx := context.Background()
			vm, err := svc.WorkspaceVM(ctx)
			if err != nil {
				t.Fatalf("WorkspaceVM: %v", err)
			}
			vm.Resources = tc.saved
			if err := svc.workspaceVM.Save(ctx, vm); err != nil {
				t.Fatalf("Save: %v", err)
			}

			if err := svc.ConvertLargeWorkspaceVM(ctx); err != nil {
				t.Fatalf("ConvertLargeWorkspaceVM: %v", err)
			}
			converted, err := svc.WorkspaceVM(ctx)
			if err != nil {
				t.Fatalf("WorkspaceVM: %v", err)
			}
			if converted.Resources != tc.want {
				t.Errorf("resources = %+v, want %+v", converted.Resources, tc.want)
			}
		})
	}
}

func TestMigration_LargeSessionKeepsItsNumbers(t *testing.T) {
	database := db.OpenForTest(t)
	svc := newServiceWithNamedRuntime(t, database, newFakeSandbox(), RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")
	if _, err := database.Exec(`UPDATE sessions SET resources = '{"size":"large"}' WHERE id = ?`, sess.ID); err != nil {
		t.Fatalf("save as large: %v", err)
	}
	migration, err := fs.ReadFile(deepcove.Migrations(), "db/migrations/0002_large_vm_size.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}

	if _, err := database.Exec(string(migration)); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	converted, err := svc.Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := (Resources{CPU: 2, MemoryMB: 4 << 10}); converted.Resources != want {
		t.Errorf("resources = %+v, want %+v", converted.Resources, want)
	}
}
