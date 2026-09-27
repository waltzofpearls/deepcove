package session

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/waltzofpearls/deepcove/internal/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// call runs one request against the workspace VM routes and returns the recorder.
func call(t *testing.T, svc *Service, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	svc.AttachRoutes(mux)

	r := httptest.NewRequest(method, "/api/workspace-vm", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func decodeWorkspaceVM(t *testing.T, w *httptest.ResponseRecorder) WorkspaceVM {
	t.Helper()
	var vm WorkspaceVM
	if err := json.Unmarshal(w.Body.Bytes(), &vm); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return vm
}

func TestWorkspaceVMEndpoints(t *testing.T) {
	// A VM runtime, because a workspace VM is refused on anything else.
	svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), &listerSandbox{fakeSandbox: newFakeSandbox()}, RuntimeAppleVF)
	root := t.TempDir()

	// Nothing recorded yet, which the UI reads as "offer to create one" rather than as a failure.
	if w := call(t, svc, http.MethodGet, ""); w.Code != http.StatusNotFound {
		t.Fatalf("GET before create: status %d, want 404", w.Code)
	}

	w := call(t, svc, http.MethodPut, `{"host_root":"`+root+`","resources":{"cpu":4,"memory_mb":8192}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: status %d, body %s", w.Code, w.Body)
	}
	saved := decodeWorkspaceVM(t, w)
	if saved.HostRoot != root || saved.Resources.CPU != 4 {
		t.Errorf("saved %+v, want the root and size sent", saved)
	}
	if saved.SandboxID != "" {
		t.Errorf("sandbox %q; the machine is built on first use, not here", saved.SandboxID)
	}

	if got := decodeWorkspaceVM(t, call(t, svc, http.MethodGet, "")); got.HostRoot != root {
		t.Errorf("GET returned %+v, want the saved record", got)
	}

	if w := call(t, svc, http.MethodDelete, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d, body %s", w.Code, w.Body)
	}
	if w := call(t, svc, http.MethodGet, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET after delete: status %d, want 404", w.Code)
	}
}

func TestWorkspaceVMEndpointErrors(t *testing.T) {
	t.Run("a folder that is not there is a 400", func(t *testing.T) {
		svc, _ := newListerService(t)
		w := call(t, svc, http.MethodPut, `{"host_root":"/no/such/folder"}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400: %s", w.Code, w.Body)
		}
	})

	t.Run("a body that is not JSON is a 400", func(t *testing.T) {
		svc, _ := newListerService(t)
		if w := call(t, svc, http.MethodPut, "not json"); w.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", w.Code)
		}
	})

	t.Run("deleting a machine in use is a 409", func(t *testing.T) {
		svc, _ := workspaceService(t)
		onWorkspace(t, svc, "first", "")

		w := call(t, svc, http.MethodDelete, "")
		if w.Code != http.StatusConflict {
			t.Errorf("status %d, want 409: %s", w.Code, w.Body)
		}
	})
}

// The shared machine sits with no sessions on it between uses, which must not read as a leak: the view
// marks it, and the leak cleanup refuses it.
func TestWorkspaceVMIsNotALeak(t *testing.T) {
	ctx := context.Background()
	svc, _ := workspaceService(t)

	vm, err := svc.EnsureWorkspaceVM(ctx)
	if err != nil {
		t.Fatalf("EnsureWorkspaceVM: %v", err)
	}

	views, err := svc.RuntimeViews(ctx)
	if err != nil {
		t.Fatalf("RuntimeViews: %v", err)
	}
	view := viewFor(t, views, RuntimeAppleVF)
	if len(view.Sandboxes) != 1 {
		t.Fatalf("got %d sandboxes, want the machine: %+v", len(view.Sandboxes), view.Sandboxes)
	}
	sb := view.Sandboxes[0]
	if !sb.IsWorkspaceVM {
		t.Error("the machine is not marked as the workspace vm")
	}
	if sb.Leaked() {
		t.Error("a workspace vm with no sessions reads as leaked")
	}

	if err := svc.DestroyLeakedSandbox(ctx, runtimeFake, vm.SandboxID); !errors.Is(err, ErrSandboxIsWorkspaceVM) {
		t.Errorf("leak cleanup returned %v, want ErrSandboxIsWorkspaceVM", err)
	}
}
