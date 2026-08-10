package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// decodeStream reads the newline delimited JSON the setup endpoint emits.
func decodeStream(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestHandleSetupRuntime(t *testing.T) {
	t.Run("streams progress and ends done", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)
		svc.SetRuntimeInstaller(RuntimeAppleVF, func(_ context.Context, report func(sandbox.PullProgress)) error {
			report(sandbox.PullProgress{Status: "Downloading kernel", Current: 5, Total: 10})
			report(sandbox.PullProgress{Status: "Downloading kernel", Current: 10, Total: 10})
			svc.MarkRuntimeReady(RuntimeAppleVF)
			return nil
		})

		mux := http.NewServeMux()
		svc.AttachRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runtimes/applevf/setup", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
			t.Errorf("content type: got %q", ct)
		}
		lines := decodeStream(t, rec.Body.String())
		last := lines[len(lines)-1]
		if last["done"] != true || last["percent"] != float64(100) {
			t.Errorf("last line: got %v, want done at 100", last)
		}
		if len(lines) < 2 {
			t.Errorf("got %d lines, want progress before done", len(lines))
		}
		// The whole point: it is usable afterwards without a restart.
		if _, still := svc.MissingRuntimes()[RuntimeAppleVF]; still {
			t.Error("the runtime is still reported as missing after a successful setup")
		}
	})

	t.Run("a failure is reported in the stream and the runtime stays missing", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)
		svc.SetRuntimeInstaller(RuntimeAppleVF, func(context.Context, func(sandbox.PullProgress)) error {
			return errors.New("registry unreachable")
		})

		mux := http.NewServeMux()
		svc.AttachRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runtimes/applevf/setup", nil))

		lines := decodeStream(t, rec.Body.String())
		last := lines[len(lines)-1]
		if !strings.Contains(last["error"].(string), "registry unreachable") {
			t.Errorf("last line: got %v, want the error", last)
		}
		if _, still := svc.MissingRuntimes()[RuntimeAppleVF]; !still {
			t.Error("a failed setup should leave the runtime missing")
		}
	})

	t.Run("setting up a runtime that is not missing is a conflict", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		mux := http.NewServeMux()
		svc.AttachRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runtimes/docker/setup", nil))
		if rec.Code != http.StatusConflict {
			t.Errorf("status: got %d, want 409", rec.Code)
		}
	})

	t.Run("a node that cannot set it up says so", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)
		mux := http.NewServeMux()
		svc.AttachRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runtimes/applevf/setup", nil))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("status: got %d, want 501", rec.Code)
		}
	})
}
