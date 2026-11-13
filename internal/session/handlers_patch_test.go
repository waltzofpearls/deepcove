package session

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/db"
)

func TestService_handlePatch(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantSize   string
	}{
		{"a size", `{"resources":{"size":"small"},"restart_now":false}`, http.StatusOK, VMSizeSmall},
		{"a field it does not know", `{"vm_size":"small","restart_now":false}`, http.StatusBadRequest, VMSizeMedium},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newServiceWithNamedRuntime(t, db.OpenForTest(t), newFakeSandbox(), RuntimeAppleVF)
			sess := mustCreate(t, svc, "claude")
			request := httptest.NewRequest(http.MethodPatch, "/api/sessions/"+sess.ID, strings.NewReader(tc.body))
			request.SetPathValue("id", sess.ID)
			recorder := httptest.NewRecorder()

			svc.handlePatch(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, tc.wantStatus, recorder.Body)
			}
			saved, err := svc.Get(request.Context(), sess.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if saved.Resources.Size != tc.wantSize {
				t.Errorf("size = %q, want %q", saved.Resources.Size, tc.wantSize)
			}
		})
	}
}
