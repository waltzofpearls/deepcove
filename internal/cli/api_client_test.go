package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/session"
)

func TestStopRunningSessions(t *testing.T) {
	cases := []struct {
		name string
		// refusing is the id of a session whose stop fails.
		refusing    string
		wantStopped int
		wantErr     bool
	}{
		{name: "every session stops", wantStopped: 2},
		{name: "one will not stop", refusing: "b", wantStopped: 1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var asked []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/sessions/local" {
					_ = json.NewEncoder(w).Encode([]*session.Session{
						{ID: "a", Name: "one", Status: session.StatusRunning},
						{ID: "b", Name: "two", Status: session.StatusRunning},
					})
					return
				}
				id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/stop")
				asked = append(asked, id)
				if id == tc.refusing {
					http.Error(w, "busy", http.StatusInternalServerError)
				}
			}))
			defer srv.Close()

			stopped, err := stopRunningSessions(context.Background(), &apiClient{baseURL: srv.URL, http: srv.Client()})
			if stopped != tc.wantStopped {
				t.Errorf("stopped %d, want %d", stopped, tc.wantStopped)
			}
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Errorf("err %v, want an error: %v", err, tc.wantErr)
			}
			// The one that refused must not stop the ones after it.
			if want := []string{"a", "b"}; !slices.Equal(asked, want) {
				t.Errorf("asked %v, want %v", asked, want)
			}
		})
	}
}
