package applevf

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSweepLeftovers(t *testing.T) {
	cases := []struct {
		name       string
		pid        string // what vm.pid holds; "" means no pid file
		locked     bool
		wantKilled bool
	}{
		{name: "a vm run still holding its lock is killed", pid: "4242", locked: true, wantKilled: true},
		{name: "a pid with no lock behind it is left alone", pid: "4242", locked: false},
		{name: "a folder with no pid file kills nothing", pid: "", locked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "abcd1234")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.pid != "" {
				if err := os.WriteFile(filepath.Join(dir, "vm.pid"), []byte(tc.pid), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var killed []int
			kill := func(pid int, _ syscall.Signal) error {
				killed = append(killed, pid)
				return nil
			}

			locked := func(string) bool { return tc.locked }
			if _, err := sweepLeftovers(root, locked, kill); err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if got := len(killed) > 0; got != tc.wantKilled {
				t.Errorf("killed %v, want killed %v", killed, tc.wantKilled)
			}
			if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Error("the leftover folder was not removed")
			}
		})
	}
}
