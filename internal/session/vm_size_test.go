package session

import (
	"context"
	"errors"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestSessionVMsThatFit(t *testing.T) {
	cases := []struct {
		name         string
		size         string
		hostMemoryMB int64
		want         int
	}{
		{"16 GB Mac, small", VMSizeSmall, 16 << 10, 12},
		{"16 GB Mac, medium", VMSizeMedium, 16 << 10, 6},
		{"16 GB Mac, large", VMSizeLarge, 16 << 10, 3},
		{"32 GB Mac, medium", VMSizeMedium, 32 << 10, 14},
		{"a host smaller than what it keeps back", VMSizeSmall, 2 << 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			size, _ := findVMSize(SessionVMSizes, tc.size)
			if got := sessionVMsThatFit(size, tc.hostMemoryMB); got != tc.want {
				t.Errorf("sessionVMsThatFit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAgentsThatFit(t *testing.T) {
	want := map[string]int{VMSizeSmall: 3, VMSizeMedium: 7, VMSizeLarge: 15}
	for _, size := range WorkspaceVMSizes {
		t.Run(size.Name, func(t *testing.T) {
			if got := agentsThatFit(size); got != want[size.Name] {
				t.Errorf("agentsThatFit = %d, want %d", got, want[size.Name])
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

func TestWorkspaceVMSpec_BootsWithTheNamedSize(t *testing.T) {
	agent, _ := resolveAgent(defaultAgent)
	vm := &WorkspaceVM{HostRoot: t.TempDir(), GuestRoot: "/workspace", Resources: Resources{Size: VMSizeLarge}}
	spec, err := workspaceVMSpec(vm, agent)
	if err != nil {
		t.Fatalf("workspaceVMSpec: %v", err)
	}
	large, _ := findVMSize(WorkspaceVMSizes, VMSizeLarge)
	if spec.Resources.Memory != large.MemoryMB<<20 || spec.Resources.CPU != float64(large.CPU) {
		t.Errorf("booted with %+v, want large's %d CPU and %d MB", spec.Resources, large.CPU, large.MemoryMB)
	}
}
