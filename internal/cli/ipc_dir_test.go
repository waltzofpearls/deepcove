//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureIPCDir(t *testing.T) {
	cases := []struct {
		name            string
		blockRuntimeDir bool
		blockTempDir    bool
		wantErr         bool
	}{
		{name: "the runtime directory"},
		{name: "the temp directory, when the runtime one cannot be used", blockRuntimeDir: true},
		{name: "nowhere", blockRuntimeDir: true, blockTempDir: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtimeDir, tempDir := t.TempDir(), t.TempDir()
			if tc.blockRuntimeDir {
				runtimeDir = notADir(t)
			}
			if tc.blockTempDir {
				tempDir = notADir(t)
			}
			t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
			t.Setenv("TMPDIR", tempDir)

			dir, err := ensureIPCDir("/home/deep/.config/deepcove")
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error when neither place works")
				}
				// A caller that ignored the error would put sockets somewhere nobody sweeps.
				if dir != "" {
					t.Errorf("got the path %q with an error", dir)
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureIPCDir: %v", err)
			}
			want := runtimeDir
			if tc.blockRuntimeDir {
				want = tempDir
			}
			if filepath.Dir(dir) != want {
				t.Errorf("got %q, want a folder in %q", dir, want)
			}
		})
	}
}

func TestEnsureIPCDir_IsPerDataDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	first, err := ensureIPCDir("/home/deep/.config/deepcove")
	if err != nil {
		t.Fatalf("ensureIPCDir: %v", err)
	}
	second, err := ensureIPCDir("/home/deep/other-deepcove")
	if err != nil {
		t.Fatalf("ensureIPCDir: %v", err)
	}
	// Two deepcoves sharing one folder would sweep each other's sessions at startup.
	if first == second {
		t.Errorf("both data dirs got %s", first)
	}
}

// A unix socket path is capped by the kernel: 104 bytes on macOS, 108 on Linux. The smaller one is
// what has to fit.
const maxSocketPath = 104

func TestEnsureIPCDir_LeavesRoomForASocket(t *testing.T) {
	dir, err := ensureIPCDir("/home/deep/.config/deepcove")
	if err != nil {
		t.Fatalf("ensureIPCDir: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("%q is not a directory: %v", dir, err)
	}
	// What a session puts under here: each runtime's own folder, then a VM's per machine folder and
	// its longest socket name, or a sandboxless session's socket.
	for _, socket := range []string{
		filepath.Join(dir, "vm", "0123abcd", "virtiofs.sock"),
		filepath.Join(dir, "tmux", "AAAAAAAA.sock"),
	} {
		if len(socket) > maxSocketPath {
			t.Errorf("%q is %d bytes, more than a socket path can be", socket, len(socket))
		}
	}
}

func TestRequirePrivateToUs(t *testing.T) {
	uid := os.Getuid()
	cases := []struct {
		name    string
		mode    os.FileMode
		asUser  int
		file    bool
		wantErr bool
	}{
		{name: "ours and closed", mode: 0o700, asUser: uid},
		{name: "ours but open to others", mode: 0o755, asUser: uid},
		{name: "someone else's", mode: 0o700, asUser: uid + 1, wantErr: true},
		{name: "not a directory", asUser: uid, file: true, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.file {
				if err := requirePrivateToUs(notADir(t), tc.asUser); err == nil {
					t.Error("want an error for a path that is not a directory")
				}
				return
			}
			dir := filepath.Join(t.TempDir(), "ipc")
			if err := os.Mkdir(dir, tc.mode); err != nil {
				t.Fatal(err)
			}
			// Mkdir applies the umask, so say it again.
			if err := os.Chmod(dir, tc.mode); err != nil {
				t.Fatal(err)
			}

			err := requirePrivateToUs(dir, tc.asUser)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			info, statErr := os.Stat(dir)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if mode := info.Mode().Perm(); mode&0o077 != 0 {
				t.Errorf("mode %o is still open to other users", mode)
			}
		})
	}
}

func notADir(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
