//go:build linux || darwin

package vm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// FolderID names a folder by identity rather than by path.
type FolderID struct {
	Dev uint64
	Ino uint64
}

// openSharedFolders opens every shared folder without following links and checks the folder it actually
// got, so a link swapped into a path after it was checked cannot point the VM somewhere else.
// closeFolders releases them once the VM holds its own.
func openSharedFolders(shares []Share, roots sandbox.ShareRoots) (opened []Share, closeFolders func(), err error) {
	opened = make([]Share, len(shares))
	var folders []*os.File
	closeFolders = func() {
		for _, folder := range folders {
			_ = folder.Close()
		}
	}
	for i, share := range shares {
		folder, id, err := openSharedFolder(share.HostPath, roots)
		if err != nil {
			closeFolders()
			return nil, func() {}, err
		}
		folders = append(folders, folder)
		share.Folder, share.FolderID = folder, id
		opened[i] = share
	}
	return opened, closeFolders, nil
}

func openSharedFolder(path string, roots sandbox.ShareRoots) (*os.File, FolderID, error) {
	fd, err := openFolderWithoutLinks(path)
	if err != nil {
		return nil, FolderID{}, fmt.Errorf("%s can't be shared: %w", path, err)
	}
	folder := os.NewFile(uintptr(fd), path)
	folderAndParents, err := parentsOfOpenFolder(fd)
	if err == nil {
		err = checkSharedFolder(folderAndParents, path, roots)
	}
	if err != nil {
		_ = folder.Close()
		return nil, FolderID{}, err
	}
	return folder, folderAndParents[0], nil
}

// checkSharedFolder applies the share rules to an opened folder, by identity rather than by name, so it
// judges what the VM will really get.
func checkSharedFolder(folderAndParents []FolderID, path string, roots sandbox.ShareRoots) error {
	folder := folderAndParents[0]
	for _, refused := range roots.Refused {
		refusedAndParents, err := parentsOfPath(sandbox.ResolveSymlinks(filepath.Clean(refused)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s can't be shared: %w", path, err)
		}
		if contains(folderAndParents, refusedAndParents[0]) || contains(refusedAndParents, folder) {
			return fmt.Errorf("%s can't be shared: %s holds deepcove's own files, and a session could change them",
				path, refused)
		}
	}
	for _, root := range roots.Allowed {
		rootAndParents, err := parentsOfPath(sandbox.ResolveSymlinks(filepath.Clean(root)))
		if err == nil && contains(folderAndParents, rootAndParents[0]) {
			return nil
		}
	}
	return fmt.Errorf("%s can't be shared: it is not inside a folder that may be shared", path)
}

// openFolderWithoutLinks opens path one folder at a time from the root, and refuses a link at any step.
// Each step starts from a folder already open, so nothing can redirect it partway.
func openFolderWithoutLinks(path string) (int, error) {
	current, err := unix.Open("/", openFolderFlags, 0)
	if err != nil {
		return -1, err
	}
	for _, name := range strings.Split(strings.Trim(path, "/"), "/") {
		if name == "" {
			continue
		}
		next, err := unix.Openat(current, name, openFolderFlags|unix.O_NOFOLLOW, 0)
		_ = unix.Close(current)
		if err != nil {
			return -1, err
		}
		current = next
	}
	return current, nil
}

func openParentFolder(fd int) (int, error) {
	return unix.Openat(fd, "..", openFolderFlags|unix.O_NOFOLLOW, 0)
}

func parentsOfOpenFolder(fd int) ([]FolderID, error) {
	var ids []FolderID
	current, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	for {
		var stat unix.Stat_t
		if err := unix.Fstat(current, &stat); err != nil {
			_ = unix.Close(current)
			return nil, err
		}
		id := FolderID{Dev: uint64(stat.Dev), Ino: stat.Ino}
		if len(ids) > 0 && ids[len(ids)-1] == id {
			_ = unix.Close(current)
			return ids, nil
		}
		ids = append(ids, id)
		parent, err := openParentFolder(current)
		_ = unix.Close(current)
		if err != nil {
			return nil, err
		}
		current = parent
	}
}

func parentsOfPath(path string) ([]FolderID, error) {
	var ids []FolderID
	for current := path; ; current = filepath.Dir(current) {
		var stat unix.Stat_t
		if err := unix.Stat(current, &stat); err != nil {
			if errors.Is(err, unix.ENOENT) {
				return nil, os.ErrNotExist
			}
			return nil, err
		}
		ids = append(ids, FolderID{Dev: uint64(stat.Dev), Ino: stat.Ino})
		if current == filepath.Dir(current) {
			return ids, nil
		}
	}
}

func contains(ids []FolderID, id FolderID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
