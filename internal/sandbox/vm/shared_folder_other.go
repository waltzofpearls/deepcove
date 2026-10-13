//go:build !linux && !darwin

package vm

import "github.com/waltzofpearls/deepcove/internal/sandbox"

// FolderID names a folder by identity rather than by path.
type FolderID struct {
	Dev uint64
	Ino uint64
}

func openSharedFolders(shares []Share, _ sandbox.ShareRoots) ([]Share, func(), error) {
	return shares, func() {}, nil
}
