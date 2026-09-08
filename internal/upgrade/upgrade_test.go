package upgrade

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch string
		want         string
	}{
		{"darwin", "arm64", "deepcove_v1.2.3_darwin-arm64.zip"},
		{"darwin", "amd64", "deepcove_v1.2.3_darwin-amd64.zip"},
		{"windows", "amd64", "deepcove_v1.2.3_windows-amd64.zip"},
		{"linux", "amd64", "deepcove_v1.2.3_linux-amd64.tar.gz"},
		{"linux", "arm64", "deepcove_v1.2.3_linux-arm64.tar.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.goos+"-"+tc.goarch, func(t *testing.T) {
			if got := AssetName("v1.2.3", tc.goos, tc.goarch); got != tc.want {
				t.Errorf("AssetName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBinaryName(t *testing.T) {
	if got := BinaryName("windows"); got != "deepcove.exe" {
		t.Errorf("windows = %q", got)
	}
	if got := BinaryName("linux"); got != "deepcove" {
		t.Errorf("linux = %q", got)
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "deepcove")
	if err := os.WriteFile(target, []byte("old"), 0o750); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, ".deepcove-new")
	if err := os.WriteFile(fresh, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Replace(target, fresh); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Errorf("mode = %v, want the mode the old binary had", info.Mode().Perm())
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Errorf("the replaced binary was left behind")
	}
}

func TestCanWrite(t *testing.T) {
	if !CanWrite(t.TempDir()) {
		t.Error("a temp dir should be writable")
	}
	if CanWrite(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Error("a missing dir is not writable")
	}
}
