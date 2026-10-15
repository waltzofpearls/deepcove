package session

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

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
	if resp.Runtimes.Unavailable[runtimeFake] == "" {
		t.Errorf("unavailable = %v, want the down runtime reported", resp.Runtimes.Unavailable)
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
		{"sandbox not found", fmt.Errorf("wrap: %w", sandbox.ErrNotFound), http.StatusNotFound, codeNotFound},
		{"session not found", fmt.Errorf("wrap: %w", ErrNotFound), http.StatusNotFound, codeNotFound},
		{"conflict", fmt.Errorf("wrap: %w", sandbox.ErrConflict), http.StatusConflict, codeConflict},
		{"an image that cannot be fetched", fmt.Errorf("wrap: %w", sandbox.ErrImageUnavailable), http.StatusBadGateway, codeImageUnavailable},
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

func TestUnavailableReason(t *testing.T) {
	cases := []struct {
		runtime string
		want    string
	}{
		{runtime: RuntimeAppleVF, want: "The virtual machine runtime isn't responding."},
		{runtime: RuntimeCloudHV, want: "The virtual machine runtime isn't responding."},
		{runtime: RuntimeSandboxless, want: "tmux isn't installed. Sessions with no sandbox need it."},
		{runtime: "something new", want: "This runtime isn't available right now."},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			if got := unavailableReason(tc.runtime); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
