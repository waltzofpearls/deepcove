package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidateSpec(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, ".config", "deepcove")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(home, "bin", "deepcove")
	if err := os.MkdirAll(filepath.Dir(program), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	notYet := filepath.Join(home, "Library", "LaunchAgents") // refused, but not created
	roots := ShareRoots{Allowed: []string{home}, Refused: []string{dataDir, program, notYet}}
	// Only the same folder on a disk that ignores case, as macOS disks usually do. Elsewhere it is a
	// different folder, and sharing it is fine.
	otherCase := filepath.Join(home, ".config", "DEEPCOVE")
	_, err = os.Stat(otherCase)
	ignoresCase := err == nil
	withSharedFolder := func(hostPath, guestPath string) SandboxSpec {
		return SandboxSpec{Image: "img", SharedFolders: []SharedFolder{{HostPath: hostPath, GuestPath: guestPath}}}
	}
	cases := []struct {
		name     string
		spec     SandboxSpec
		wantErr  bool
		wantText string // part of the error, when the wording matters to the user
	}{
		{name: "no folders", spec: SandboxSpec{Image: "img"}},
		{name: "a folder inside an allowed root", spec: withSharedFolder(home+"/code", "/workspace")},
		{name: "a folder that only shares a prefix with a refused one", spec: withSharedFolder(home+"/.configx", "/workspace")},
		{name: "no image", spec: SandboxSpec{}, wantErr: true},
		{name: "a folder outside every root", spec: withSharedFolder("/etc", "/workspace"), wantErr: true},
		{name: "a sibling that only shares a prefix", spec: withSharedFolder(home+"2", "/workspace"), wantErr: true},
		{name: "a relative host path", spec: withSharedFolder("code", "/workspace"), wantErr: true},
		{name: "a host path not in cleaned form", spec: withSharedFolder(home+"/../other", "/workspace"), wantErr: true},
		{name: "an empty host path", spec: withSharedFolder("", "/workspace"), wantErr: true},
		{name: "a relative guest path", spec: withSharedFolder(home+"/code", "workspace"), wantErr: true},
		{
			name: "a link inside the root that points out of it", spec: withSharedFolder(home+"/escape/etc", "/workspace"),
			wantErr: true, wantText: "it is a link to",
		},
		{name: "the refused folder", spec: withSharedFolder(dataDir, "/workspace"), wantErr: true},
		{name: "a folder inside the refused one", spec: withSharedFolder(dataDir+"/tailnet", "/workspace"), wantErr: true},
		{name: "the allowed root, which holds the refused folder", spec: withSharedFolder(home, "/workspace"), wantErr: true},
		{name: "a parent of the refused folder", spec: withSharedFolder(home+"/.config", "/workspace"), wantErr: true},
		{name: "the folder that holds a refused file", spec: withSharedFolder(home+"/bin", "/workspace"), wantErr: true},
		{
			name:    "a refused folder that does not exist yet, typed in other case",
			spec:    withSharedFolder(filepath.Join(home, "library", "launchagents"), "/workspace"),
			wantErr: runtime.GOOS == "darwin",
		},
		{name: "the refused folder typed in other case", spec: withSharedFolder(otherCase, "/workspace"), wantErr: ignoresCase},
		{name: "a folder inside it typed in other case", spec: withSharedFolder(otherCase+"/tailnet", "/workspace"), wantErr: ignoresCase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSpec(tc.spec, roots)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateSpec() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantText != "" && (err == nil || !strings.Contains(err.Error(), tc.wantText)) {
				t.Errorf("error = %v, want it to say %q", err, tc.wantText)
			}
		})
	}
}
