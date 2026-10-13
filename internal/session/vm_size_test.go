package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

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
		{"16 GB Mac, large", VMSizeLarge, 16 << 10, macKeptBackMB, 3},
		{"32 GB Mac, medium", VMSizeMedium, 32 << 10, macKeptBackMB, 14},
		{"4 GB Linux server, small", VMSizeSmall, 3915, linuxKeptBackMB, 2},
		{"4 GB Linux server, medium", VMSizeMedium, 3915, linuxKeptBackMB, 1},
		{"4 GB Linux server, large", VMSizeLarge, 3915, linuxKeptBackMB, 0},
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
			{Name: VMSizeLarge, CPU: 8, MemoryMB: 16 << 10},
		}},
		{"512 MB Linux", 512, linuxKeptBackMB, nil},
		{"2 GB Linux", 1946, linuxKeptBackMB, []VMSize{{Name: VMSizeSmall, CPU: 1, MemoryMB: 1 << 10}}},
		{"2.5 GB Linux, no scaled size big enough", 2560, linuxKeptBackMB, []VMSize{
			{Name: VMSizeSmall, CPU: 1, MemoryMB: 1 << 10},
		}},
		{"4 GB Linux", 3915, linuxKeptBackMB, []VMSize{{Name: VMSizeLarge, CPU: 1, MemoryMB: 2 << 10}}},
		{"16 GB Mac", 16 << 10, macKeptBackMB, []VMSize{
			{Name: VMSizeSmall, CPU: 1, MemoryMB: 3 << 10},
			{Name: VMSizeMedium, CPU: 3, MemoryMB: 6 << 10},
			{Name: VMSizeLarge, CPU: 6, MemoryMB: 12 << 10},
		}},
		{"32 GB Mac, capped", 32 << 10, macKeptBackMB, []VMSize{
			{Name: VMSizeSmall, CPU: 2, MemoryMB: 4 << 10},
			{Name: VMSizeMedium, CPU: 4, MemoryMB: 8 << 10},
			{Name: VMSizeLarge, CPU: 8, MemoryMB: 16 << 10},
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
		{"lacks it, takes the biggest", workspaceVMSizes(3915, 1<<10), VMSizeLarge},
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

func TestValidateVMSize(t *testing.T) {
	cases := []struct {
		name      string
		resources Resources
		wantErr   bool
	}{
		{"no size", Resources{}, false},
		{"a known size", Resources{Size: VMSizeSmall}, false},
		{"an unknown size", Resources{Size: "huge"}, true},
		{"a size and numbers", Resources{Size: VMSizeSmall, CPU: 4}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVMSize(tc.resources, SessionVMSizes)
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
		{"large", Resources{Size: VMSizeLarge}, Resources{Size: VMSizeLarge, CPU: 2, MemoryMB: 4 << 10}},
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
		name     string
		asked    Resources
		wantSize string
		wantErr  error
	}{
		{"no size gets the default", Resources{}, DefaultVMSize, nil},
		{"a picked size is kept", Resources{Size: VMSizeLarge}, VMSizeLarge, nil},
		{"an unknown size is refused", Resources{Size: "huge"}, "", ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lister := &listerSandbox{fakeSandbox: newFakeSandbox()}
			svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), lister, RuntimeAppleVF)
			vm, err := svc.SaveWorkspaceVM(context.Background(), WorkspaceVMRequest{HostRoot: t.TempDir(), Resources: tc.asked})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SaveWorkspaceVM err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && vm.Resources.Size != tc.wantSize {
				t.Errorf("size = %q, want %q", vm.Resources.Size, tc.wantSize)
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
		{"a size only a bigger host has boots as the default", workspaceVMSizes(3915, 1<<10), VMSizeMedium, VMSizeLarge},
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
