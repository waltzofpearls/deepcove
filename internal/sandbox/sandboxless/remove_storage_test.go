//go:build !windows

package sandboxless

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStorage(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the session id to remove, and a file that must survive.
		setup   func(t *testing.T, d *Driver) (sessionID, mustSurvive string)
		wantErr bool
		// gone is the folder that should be removed, relative to the workspaces folder.
		gone string
	}{
		{
			name: "our folder goes",
			setup: func(t *testing.T, d *Driver) (string, string) {
				workspace(t, d, "s-1")
				return "s-1", write(t, filepath.Join(t.TempDir(), "yours.txt"))
			},
			gone: "s-1",
		},
		{
			name: "a folder you picked is untouched",
			setup: func(t *testing.T, d *Driver) (string, string) {
				yours := t.TempDir()
				return "s-1", write(t, filepath.Join(yours, "code.txt"))
			},
		},
		{
			// A folder you picked, sitting inside the workspaces folder. Destroying your session still
			// only reaches the folder named after it, an id deepcove minted after you picked.
			name: "a folder you picked inside the workspaces folder is untouched",
			setup: func(t *testing.T, d *Driver) (string, string) {
				yours := workspace(t, d, "s-you-picked-this")
				return "s-1", write(t, filepath.Join(yours, "code.txt"))
			},
		},
		{
			name: "a link out of the workspaces folder is refused",
			setup: func(t *testing.T, d *Driver) (string, string) {
				yours := t.TempDir()
				kept := write(t, filepath.Join(yours, "code.txt"))
				if err := os.MkdirAll(filepath.Join(d.stateDir, "workspaces"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(yours, filepath.Join(d.stateDir, "workspaces", "s-1")); err != nil {
					t.Fatal(err)
				}
				return "s-1", kept
			},
			wantErr: true,
		},
		{
			name: "a session id that climbs out is refused",
			setup: func(t *testing.T, d *Driver) (string, string) {
				workspace(t, d, "s-1")
				return "../../..", write(t, filepath.Join(t.TempDir(), "yours.txt"))
			},
			wantErr: true,
		},
		{
			name: "an empty session id is refused",
			setup: func(t *testing.T, d *Driver) (string, string) {
				return "", write(t, filepath.Join(t.TempDir(), "yours.txt"))
			},
			wantErr: true,
		},
		{
			name: "the workspaces folder itself, asked for as .",
			setup: func(t *testing.T, d *Driver) (string, string) {
				workspace(t, d, "s-1")
				return ".", filepath.Join(d.stateDir, "workspaces")
			},
			wantErr: true,
		},
		{
			name: "the workspaces folder itself, asked for as ..",
			setup: func(t *testing.T, d *Driver) (string, string) {
				workspace(t, d, "s-1")
				return "..", filepath.Join(d.stateDir, "workspaces")
			},
			wantErr: true,
		},
		{
			name: "the workspaces folder itself, asked for as /",
			setup: func(t *testing.T, d *Driver) (string, string) {
				workspace(t, d, "s-1")
				return "/", filepath.Join(d.stateDir, "workspaces")
			},
			wantErr: true,
		},
		{
			name: "a session called workspaces, which is not the folder of that name",
			setup: func(t *testing.T, d *Driver) (string, string) {
				workspace(t, d, "s-1")
				return "workspaces", filepath.Join(d.stateDir, "workspaces")
			},
		},
		{
			name: "a folder that is not there is not an error",
			setup: func(t *testing.T, d *Driver) (string, string) {
				return "s-never-existed", write(t, filepath.Join(t.TempDir(), "yours.txt"))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDriver(t)
			sessionID, mustSurvive := tc.setup(t, d)

			err := d.RemoveStorage(context.Background(), sessionID)
			if tc.wantErr != (err != nil) {
				t.Fatalf("RemoveStorage: got %v, wantErr %v", err, tc.wantErr)
			}
			// Each case puts something real where it must survive: somebody's code, or the folder every
			// other session's folder sits in.
			if _, err := os.Stat(mustSurvive); err != nil {
				t.Fatalf("a file outside our folder was removed: %v", err)
			}
			if tc.gone != "" {
				if _, err := os.Stat(filepath.Join(d.stateDir, "workspaces", tc.gone)); !os.IsNotExist(err) {
					t.Errorf("our own folder is still there: %v", err)
				}
			}
		})
	}
}

// workspace makes the folder this driver would make for a session, with a file in it.
func workspace(t *testing.T, d *Driver, sessionID string) string {
	t.Helper()
	dir := filepath.Join(d.stateDir, "workspaces", sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "scratch.txt"))
	return dir
}

func write(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
