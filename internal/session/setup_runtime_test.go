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
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runtimes/sandboxless/setup", nil))
		if rec.Code != http.StatusConflict {
			t.Errorf("status: got %d, want 409", rec.Code)
		}
	})

	t.Run("setting up a runtime that is already set up ends done", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		mux := http.NewServeMux()
		svc.AttachRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runtimes/"+runtimeFake+"/setup", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		lines := decodeStream(t, rec.Body.String())
		if last := lines[len(lines)-1]; last["done"] != true {
			t.Errorf("last line: got %v, want done", last)
		}
	})

	t.Run("a second setup while one is running is a conflict", func(t *testing.T) {
		svc, _, _, _ := newTestService(t)
		svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)
		started, release := make(chan struct{}), make(chan struct{})
		svc.SetRuntimeInstaller(RuntimeAppleVF, func(context.Context, func(sandbox.PullProgress)) error {
			close(started)
			<-release
			return nil
		})
		mux := http.NewServeMux()
		svc.AttachRoutes(mux)

		firstDone := make(chan struct{})
		go func() {
			defer close(firstDone)
			mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/runtimes/applevf/setup", nil))
		}()
		<-started

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/runtimes/applevf/setup", nil))
		close(release)
		<-firstDone

		if rec.Code != http.StatusConflict {
			t.Errorf("status: got %d, want 409", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "already being set up") {
			t.Errorf("body: got %q", rec.Body.String())
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

func TestStartRuntimeSetup(t *testing.T) {
	cases := []struct {
		name    string
		before  func(t *testing.T, svc *Service)
		wantErr error
	}{
		{name: "a missing runtime nobody is setting up", before: func(*testing.T, *Service) {}},
		{
			name: "a setup is already running", wantErr: errRuntimeBeingSetUp,
			before: func(t *testing.T, svc *Service) { startAppleVFSetupForTest(t, svc) },
		},
		{
			name:   "a setup that failed lets the next one in",
			before: func(t *testing.T, svc *Service) { startAppleVFSetupForTest(t, svc)() },
		},
		{
			name: "a setup that worked leaves nothing to do", wantErr: errRuntimeNotMissing,
			before: func(t *testing.T, svc *Service) {
				finish := startAppleVFSetupForTest(t, svc)
				svc.MarkRuntimeReady(RuntimeAppleVF)
				finish()
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newTestService(t)
			svc.MarkRuntimeMissing(RuntimeAppleVF, MissingKernel)
			tc.before(t, svc)
			finish, err := svc.startRuntimeSetup(RuntimeAppleVF)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if finish != nil {
				finish()
			}
		})
	}
}

func startAppleVFSetupForTest(t *testing.T, svc *Service) func() {
	t.Helper()
	finish, err := svc.startRuntimeSetup(RuntimeAppleVF)
	if err != nil {
		t.Fatal(err)
	}
	return finish
}
