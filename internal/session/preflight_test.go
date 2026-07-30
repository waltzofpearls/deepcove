package session

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestService_DockerStatus(t *testing.T) {
	t.Run("reachable", func(t *testing.T) {
		svc, fake, _, _ := newTestService(t)
		fake.pingVersion = "1.51"
		st := svc.dockerStatus(context.Background())
		if !st.Reachable {
			t.Fatalf("want reachable, got %+v", st)
		}
		if st.Version != "1.51" {
			t.Errorf("version = %q, want 1.51", st.Version)
		}
	})

	t.Run("unreachable wraps ErrUnavailable", func(t *testing.T) {
		svc, fake, _, _ := newTestService(t)
		fake.pingErr = fmt.Errorf("%w: dial unix: connect: connection refused", sandbox.ErrUnavailable)
		st := svc.dockerStatus(context.Background())
		if st.Reachable {
			t.Fatalf("want unreachable, got %+v", st)
		}
		if st.Message == "" {
			t.Error("want a human-facing message when unreachable")
		}
	})
}

func TestHandlePreflight(t *testing.T) {
	svc, fake, _, _ := newTestService(t)
	fake.pingErr = sandbox.ErrUnavailable

	rec := httptest.NewRecorder()
	svc.handlePreflight(rec, httptest.NewRequest(http.MethodGet, "/api/preflight", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp PreflightResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Docker.Reachable {
		t.Errorf("docker.reachable = true, want false")
	}
}

func TestWriteSandboxError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"unavailable", fmt.Errorf("wrap: %w", sandbox.ErrUnavailable), http.StatusServiceUnavailable, codeRuntimeUnavailable},
		{"image not found", fmt.Errorf("wrap: %w", sandbox.ErrImageNotFound), http.StatusUnprocessableEntity, codeImageNotFound},
		{"sandbox not found", fmt.Errorf("wrap: %w", sandbox.ErrNotFound), http.StatusNotFound, codeNotFound},
		{"session not found", fmt.Errorf("wrap: %w", ErrNotFound), http.StatusNotFound, codeNotFound},
		{"conflict", fmt.Errorf("wrap: %w", sandbox.ErrConflict), http.StatusConflict, codeConflict},
		{"plain input error", fmt.Errorf("bad mount target"), http.StatusBadRequest, codeInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeSandboxError(rec, tc.err)
			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", body.Code, tc.wantCode)
			}
		})
	}
}
