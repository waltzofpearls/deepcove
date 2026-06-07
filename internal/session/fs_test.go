package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestHandleFsList(t *testing.T) {
	// Scope the home root to a temp dir so the listing is deterministic and confined.
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{"Dev", "Documents", ".hidden"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "Dev", "myapp"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	svc := &Service{}

	tests := []struct {
		name       string
		path       string   // "" = no ?path param (defaults to home)
		wantStatus int
		wantPath   string   // checked on 200 only; "" = skip
		wantParent string   // checked on 200 only
		wantNames  []string // checked on 200 only; directories, sorted
	}{
		{
			name:       "home root lists directories only, no parent",
			path:       "",
			wantStatus: http.StatusOK,
			wantPath:   home,
			wantParent: "",
			wantNames:  []string{".hidden", "Dev", "Documents"}, // note.txt excluded
		},
		{
			name:       "subdir lists children with parent set",
			path:       filepath.Join(home, "Dev"),
			wantStatus: http.StatusOK,
			wantParent: home,
			wantNames:  []string{"myapp"},
		},
		{
			name:       "path outside home is forbidden",
			path:       "/etc",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "nonexistent path is 404",
			path:       filepath.Join(home, "nope"),
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/fs/list"
			if tc.path != "" {
				url += "?path=" + tc.path
			}
			rec := httptest.NewRecorder()
			svc.handleFsList(rec, httptest.NewRequest(http.MethodGet, url, nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: want %d, got %d (%s)", tc.wantStatus, rec.Code, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			var body FsListResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
			}
			if tc.wantPath != "" && body.Path != tc.wantPath {
				t.Errorf("path: want %q, got %q", tc.wantPath, body.Path)
			}
			if body.Parent != tc.wantParent {
				t.Errorf("parent: want %q, got %q", tc.wantParent, body.Parent)
			}
			var names []string
			for _, e := range body.Entries {
				names = append(names, e.Name)
			}
			if !slices.Equal(names, tc.wantNames) {
				t.Errorf("entries: want %v, got %v", tc.wantNames, names)
			}
		})
	}
}
