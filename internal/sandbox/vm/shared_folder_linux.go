//go:build linux

package vm

import "golang.org/x/sys/unix"

// openFolderFlags opens a folder as a handle only, which needs no permission to read it.
const openFolderFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC
