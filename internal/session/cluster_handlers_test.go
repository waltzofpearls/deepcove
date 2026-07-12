package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleListLocalStampsClusterHeader(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	svc.SetClusterStore(NewClusterStore(nil, "prod"))

	r := httptest.NewRequest(http.MethodGet, "/api/sessions/local", nil)
	w := httptest.NewRecorder()
	svc.handleListLocal(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get(ClusterHeader); got != "prod" {
		t.Errorf("%s = %q, want prod", ClusterHeader, got)
	}
	if body := strings.TrimSpace(w.Body.String()); !strings.HasPrefix(body, "[") {
		t.Errorf("body = %q, want a bare array", body)
	}
}

func TestHandleGetSetCluster(t *testing.T) {
	svc := &Service{}
	svc.SetClusterStore(NewClusterStore(nil, "dev"))

	r := httptest.NewRequest(http.MethodGet, "/api/cluster", nil)
	w := httptest.NewRecorder()
	svc.handleGetCluster(w, r)
	if got := decodeCluster(t, w); got != "dev" {
		t.Errorf("GET cluster = %q, want dev", got)
	}

	cases := []struct {
		name        string
		body        string
		wantStatus  int
		wantCluster string // checked on 200
	}{
		{"named", `{"cluster":"prod"}`, http.StatusOK, "prod"},
		{"trimmed", `{"cluster":"  staging  "}`, http.StatusOK, "staging"},
		{"empty resets to default", `{"cluster":""}`, http.StatusOK, DefaultCluster},
		{"reserved sentinel folds to default", `{"cluster":"all"}`, http.StatusOK, DefaultCluster},
		{"too long -> 400", `{"cluster":"` + strings.Repeat("x", MaxClusterNameLen+1) + `"}`, http.StatusBadRequest, ""},
		{"malformed json -> 400", `{`, http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, "/api/cluster", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			svc.handleSetCluster(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK {
				if got := decodeCluster(t, w); got != tc.wantCluster {
					t.Errorf("cluster = %q, want %q", got, tc.wantCluster)
				}
			}
		})
	}
}

func decodeCluster(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Cluster string `json:"cluster"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode cluster response: %v", err)
	}
	return out.Cluster
}
