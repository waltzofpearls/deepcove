package session

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestService_ResizeVM(t *testing.T) {
	cases := []struct {
		name          string
		resized       Resources
		stopFirst     bool
		wantErr       error
		wantRestarted bool
		wantNoSandbox bool
	}{
		{name: "a running VM restarts at the new size", resized: Resources{Size: VMSizeSmall}, wantRestarted: true},
		{name: "a stopped VM is rebuilt on its next start", resized: Resources{Size: VMSizeSmall}, stopFirst: true, wantNoSandbox: true},
		{name: "a custom size", resized: Resources{CPU: 1, MemoryMB: 3 << 10}, wantRestarted: true},
		{name: "an unknown size is refused", resized: Resources{Size: "huge"}, wantErr: ErrInvalidInput},
		{name: "no size at all is refused", resized: Resources{}, wantErr: ErrInvalidInput},
		{name: "more cpu than the node has is refused", resized: Resources{CPU: float64(runtime.NumCPU() + 1), MemoryMB: 1 << 10}, wantErr: ErrInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
			svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
			sess := mustCreate(t, svc, "claude")
			if tc.stopFirst {
				if err := svc.Stop(ctx, sess.ID); err != nil {
					t.Fatalf("setup Stop: %v", err)
				}
			}

			_, err := svc.ResizeVM(ctx, sess.ID, tc.resized)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ResizeVM err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			saved, err := svc.Get(ctx, sess.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if saved.Resources != tc.resized {
				t.Errorf("saved resources = %+v, want %+v", saved.Resources, tc.resized)
			}
			if restarted := len(driver.specs) == 1; restarted != tc.wantRestarted {
				t.Fatalf("restarted = %v, want %v", restarted, tc.wantRestarted)
			}
			wantMemoryMB := tc.resized.withSizeApplied(SessionVMSizes).MemoryMB
			if tc.wantRestarted && driver.specs[0].Resources.Memory != wantMemoryMB<<20 {
				t.Errorf("restarted with %d bytes, want %d MB", driver.specs[0].Resources.Memory, wantMemoryMB)
			}
			if tc.wantNoSandbox && saved.SandboxID != "" {
				t.Errorf("sandbox = %q, want none so the next Start builds a VM at the new size", saved.SandboxID)
			}
		})
	}
}

func TestService_ResizeVM_RefusesASessionOnTheWorkspaceVM(t *testing.T) {
	svc, _ := workspaceService(t)
	sess := onWorkspace(t, svc, "shared", "")

	if _, err := svc.ResizeVM(context.Background(), sess.ID, Resources{Size: VMSizeSmall}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ResizeVM err = %v, want ErrInvalidInput", err)
	}
}

func TestService_SaveVMSize_LeavesARunningVMAlone(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")

	saved, err := svc.SaveVMSize(context.Background(), sess.ID, Resources{Size: VMSizeSmall})
	if err != nil {
		t.Fatalf("SaveVMSize: %v", err)
	}
	if saved.Resources.Size != VMSizeSmall || saved.SandboxID != sess.SandboxID {
		t.Errorf("got size %q on %q, want small on the same sandbox %q", saved.Resources.Size, saved.SandboxID, sess.SandboxID)
	}
	if len(driver.specs) != 0 {
		t.Errorf("restarted %d times, want none", len(driver.specs))
	}
}

func TestService_ResizeVM_RefusesWhileStarting(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")
	// A start is under way.
	done, _ := svc.beginStarting(sess.ID)
	defer done()

	if _, err := svc.ResizeVM(context.Background(), sess.ID, Resources{Size: VMSizeSmall}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ResizeVM err = %v, want it refused while starting", err)
	}
	saved, _ := svc.Get(context.Background(), sess.ID)
	if saved.SandboxID != sess.SandboxID || saved.Resources.Size == VMSizeSmall {
		t.Errorf("got size %q on %q, want nothing changed while starting", saved.Resources.Size, saved.SandboxID)
	}
}

func TestService_SaveVMSize_NextBootGetsTheNewSize(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")

	if _, err := svc.SaveVMSize(context.Background(), sess.ID, Resources{Size: VMSizeSmall}); err != nil {
		t.Fatalf("SaveVMSize: %v", err)
	}
	small, _ := findVMSize(SessionVMSizes, VMSizeSmall)
	if len(driver.nextBootSpecs) != 1 || driver.nextBootSpecs[0].Resources.Memory != small.MemoryMB<<20 {
		t.Fatalf("next boot specs = %+v, want one with small's %d MB", driver.nextBootSpecs, small.MemoryMB)
	}
	if len(driver.specs) != 0 {
		t.Errorf("restarted %d times, want none", len(driver.specs))
	}
}

func TestService_ResizeVM_RestartsASizeSavedForLater(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")
	if _, err := svc.SaveVMSize(context.Background(), sess.ID, Resources{Size: VMSizeSmall}); err != nil {
		t.Fatalf("SaveVMSize: %v", err)
	}

	if _, err := svc.ResizeVM(context.Background(), sess.ID, Resources{Size: VMSizeSmall}); err != nil {
		t.Fatalf("ResizeVM: %v", err)
	}
	if len(driver.specs) != 1 {
		t.Errorf("restarted %d times, want 1: the saved size was not in use yet", len(driver.specs))
	}
}

func TestService_ResizeVM_AFailedDropSavesNothing(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")
	if err := svc.Stop(context.Background(), sess.ID); err != nil {
		t.Fatalf("setup Stop: %v", err)
	}
	driver.destroyErr = errors.New("disk busy")

	if _, err := svc.ResizeVM(context.Background(), sess.ID, Resources{Size: VMSizeSmall}); err == nil {
		t.Fatal("ResizeVM succeeded, want the drop's failure")
	}
	saved, _ := svc.Get(context.Background(), sess.ID)
	if saved.Resources.Size == VMSizeSmall || saved.SandboxID != sess.SandboxID {
		t.Errorf("got size %q on %q, want nothing saved so a retry starts over", saved.Resources.Size, saved.SandboxID)
	}
}
