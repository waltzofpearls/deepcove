//go:build linux || darwin

package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

func TestOpenSharedFolders(t *testing.T) {
	home := sandbox.ResolveSymlinks(t.TempDir())
	project := filepath.Join(home, "project")
	dataDir := filepath.Join(home, "project", "deepcove")
	outside := sandbox.ResolveSymlinks(t.TempDir())
	sibling := filepath.Join(home, "other")
	link := filepath.Join(home, "link")
	linkedParent := filepath.Join(home, "linked-parent")
	for _, dir := range []string{dataDir, filepath.Join(dataDir, "logs"), sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(dataDir, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, linkedParent); err != nil {
		t.Fatal(err)
	}
	roots := sandbox.ShareRoots{Allowed: []string{home}, Refused: []string{dataDir}}

	cases := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "a project folder", path: sibling},
		{name: "deepcove's own folder", path: dataDir, wantErr: "holds deepcove's own files"},
		{name: "inside deepcove's folder", path: filepath.Join(dataDir, "logs"), wantErr: "holds deepcove's own files"},
		{name: "a folder holding deepcove's", path: project, wantErr: "holds deepcove's own files"},
		{name: "outside the allowed folders", path: outside, wantErr: "not inside a folder that may be shared"},
		{name: "a link", path: link, wantErr: "can't be shared"},
		{name: "a link partway along the path", path: filepath.Join(linkedParent, "other"), wantErr: "can't be shared"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opened, closeFolders, err := openSharedFolders([]Share{{HostPath: tc.path}}, roots)
			defer closeFolders()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("got %v, want an error containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if stat, err := opened[0].Folder.Stat(); err != nil || !stat.IsDir() {
				t.Fatalf("the share was not opened as a folder: %v", err)
			}
			if opened[0].FolderID == (FolderID{}) {
				t.Error("the share has no folder identity")
			}
		})
	}
}
