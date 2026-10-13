package applevf

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/waltzofpearls/deepcove/internal/sandbox/vm"
)

func TestFolderByID_FollowsTheFolderAfterItsNameChanges(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	var stat unix.Stat_t
	if err := unix.Stat(project, &stat); err != nil {
		t.Fatal(err)
	}
	id := vm.FolderID{Dev: uint64(stat.Dev), Ino: stat.Ino}
	if err := os.Rename(project, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}

	var got unix.Stat_t
	if err := unix.Stat(folderByID(id), &got); err != nil {
		t.Fatalf("stat %s: %v", folderByID(id), err)
	}
	if got.Ino != stat.Ino {
		t.Errorf("%s reached inode %d, want the checked folder %d", folderByID(id), got.Ino, stat.Ino)
	}
}
