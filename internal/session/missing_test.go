package session

import (
	"context"
	"testing"
)

func TestMissingRuntimes(t *testing.T) {
	t.Run("a node with nothing missing reports nothing", func(t *testing.T) {
		svc := &Service{}
		if got := svc.MissingRuntimes(); got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("a missing runtime is reported with its reason", func(t *testing.T) {
		svc := &Service{}
		svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)
		got := svc.MissingRuntimes()
		if got[RuntimeAppleVF] != MissingKernel {
			t.Errorf("got %q, want %q", got[RuntimeAppleVF], MissingKernel)
		}
	})

	// Callers should not be able to reach in and change what the node reports.
	t.Run("the returned map is a copy", func(t *testing.T) {
		svc := &Service{}
		svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)
		svc.MissingRuntimes()[RuntimeAppleVF] = "tampered"
		if got := svc.MissingRuntimes()[RuntimeAppleVF]; got != MissingKernel {
			t.Errorf("got %q, want %q", got, MissingKernel)
		}
	})

	// A missing runtime has no driver, so nothing may be created on it.
	t.Run("missing is not the same as registered", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)
		for _, name := range svc.Runtimes() {
			if name == RuntimeAppleVF {
				t.Fatal("a missing runtime was registered; a session could land on it")
			}
		}
	})
}

// The runtimes page should show what the node could have, not only what it has.
func TestRuntimeViewsIncludeMissingRuntimes(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)

	views, err := svc.RuntimeViews(context.Background())
	if err != nil {
		t.Fatalf("RuntimeViews: %v", err)
	}

	var found *RuntimeView
	for i := range views {
		if views[i].Name == RuntimeAppleVF {
			found = &views[i]
		}
	}
	if found == nil {
		t.Fatal("the missing runtime is absent from the view, so the user cannot see the gap")
	}
	if found.Missing != MissingKernel {
		t.Errorf("reason: got %q, want %q", found.Missing, MissingKernel)
	}
	if found.Reachable {
		t.Error("a missing runtime must not read as reachable")
	}
	// The runtimes page ranges over these, so nil would be a template error rather than an empty list.
	if found.Sandboxes == nil || found.Orphaned == nil {
		t.Error("sandbox and orphan lists should be empty, not nil")
	}
}
