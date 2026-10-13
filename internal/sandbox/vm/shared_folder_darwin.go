//go:build darwin

package vm

import "golang.org/x/sys/unix"

const openFolderFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
