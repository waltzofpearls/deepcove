package session

import (
	"context"
	"errors"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestService_ResizeVM(t *testing.T) {
	cases := []struct {
		name          string
		size          string
		stopFirst     bool
		wantErr       error
		wantRestarted bool
		wantNoSandbox bool
	}{
		{name: "a running VM restarts at the new size", size: VMSizeLarge, wantRestarted: true},
		{name: "a stopped VM is rebuilt on its next start", size: VMSizeLarge, stopFirst: true, wantNoSandbox: true},
		{name: "an unknown size is refused", size: "huge", wantErr: ErrInvalidInput},
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

			_, err := svc.ResizeVM(ctx, sess.ID, tc.size)
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
			if saved.Resources.Size != tc.size {
				t.Errorf("saved size = %q, want %q", saved.Resources.Size, tc.size)
			}
			if restarted := len(driver.specs) == 1; restarted != tc.wantRestarted {
				t.Fatalf("restarted = %v, want %v", restarted, tc.wantRestarted)
			}
			large, _ := findVMSize(SessionVMSizes, VMSizeLarge)
			if tc.wantRestarted && driver.specs[0].Resources.Memory != large.MemoryMB<<20 {
				t.Errorf("restarted with %d bytes, want large's %d MB", driver.specs[0].Resources.Memory, large.MemoryMB)
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

	if _, err := svc.ResizeVM(context.Background(), sess.ID, VMSizeLarge); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ResizeVM err = %v, want ErrInvalidInput", err)
	}
}

func TestService_SaveVMSize_LeavesARunningVMAlone(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")

	saved, err := svc.SaveVMSize(context.Background(), sess.ID, VMSizeLarge)
	if err != nil {
		t.Fatalf("SaveVMSize: %v", err)
	}
	if saved.Resources.Size != VMSizeLarge || saved.SandboxID != sess.SandboxID {
		t.Errorf("got size %q on %q, want large on the same sandbox %q", saved.Resources.Size, saved.SandboxID, sess.SandboxID)
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

	if _, err := svc.ResizeVM(context.Background(), sess.ID, VMSizeLarge); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ResizeVM err = %v, want it refused while starting", err)
	}
	saved, _ := svc.Get(context.Background(), sess.ID)
	if saved.SandboxID != sess.SandboxID || saved.Resources.Size == VMSizeLarge {
		t.Errorf("got size %q on %q, want nothing changed while starting", saved.Resources.Size, saved.SandboxID)
	}
}

func TestService_SaveVMSize_NextBootGetsTheNewSize(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")

	if _, err := svc.SaveVMSize(context.Background(), sess.ID, VMSizeLarge); err != nil {
		t.Fatalf("SaveVMSize: %v", err)
	}
	large, _ := findVMSize(SessionVMSizes, VMSizeLarge)
	if len(driver.nextBootSpecs) != 1 || driver.nextBootSpecs[0].Resources.Memory != large.MemoryMB<<20 {
		t.Fatalf("next boot specs = %+v, want one with large's %d MB", driver.nextBootSpecs, large.MemoryMB)
	}
	if len(driver.specs) != 0 {
		t.Errorf("restarted %d times, want none", len(driver.specs))
	}
}

func TestService_ResizeVM_RestartsASizeSavedForLater(t *testing.T) {
	driver := &restartingSandbox{fakeSandbox: newFakeSandbox()}
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), driver, RuntimeAppleVF)
	sess := mustCreate(t, svc, "claude")
	if _, err := svc.SaveVMSize(context.Background(), sess.ID, VMSizeLarge); err != nil {
		t.Fatalf("SaveVMSize: %v", err)
	}

	if _, err := svc.ResizeVM(context.Background(), sess.ID, VMSizeLarge); err != nil {
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

	if _, err := svc.ResizeVM(context.Background(), sess.ID, VMSizeLarge); err == nil {
		t.Fatal("ResizeVM succeeded, want the drop's failure")
	}
	saved, _ := svc.Get(context.Background(), sess.ID)
	if saved.Resources.Size == VMSizeLarge || saved.SandboxID != sess.SandboxID {
		t.Errorf("got size %q on %q, want nothing saved so a retry starts over", saved.Resources.Size, saved.SandboxID)
	}
}
