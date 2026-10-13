//go:build linux

package cloudhv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

func TestStartVirtiofsd_RunsInTheOpenedFolder(t *testing.T) {
	dir := shortTempDir(t)
	shared := t.TempDir()
	folder, err := os.Open(shared)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = folder.Close() }()
	stub := filepath.Join(dir, "vfsd")
	script := "#!/bin/sh\npwd -P > " + stub + ".cwd\necho \"$@\" > " + stub + ".args\n" +
		"for a in \"$@\"; do case \"$a\" in */*.sock) : > \"$a\" ;; esac; done\nsleep 30\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	helper, err := startVirtiofsd(stub, filepath.Join(dir, "fs0.sock"), vm.Share{Tag: "dcshare0", HostPath: "/changed", Folder: folder})
	if err != nil {
		t.Fatalf("startVirtiofsd: %v", err)
	}
	defer func() { _ = helper.Stop() }()

	if args := readArgs(t, dir, "vfsd"); !strings.Contains(args, "--shared-dir .") {
		t.Errorf("virtiofsd was not pointed at its own folder: %s", args)
	}
	cwd, err := os.ReadFile(stub + ".cwd")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(shared)
	if got := strings.TrimSpace(string(cwd)); got != want {
		t.Errorf("virtiofsd ran in %s, want the opened folder %s", got, want)
	}
}
