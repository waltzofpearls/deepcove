//go:build !windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// ensureIPCDir makes the folder a session is reached through: a VM's sockets and console FIFO, a
// sandboxless session's tmux server. Not the data dir, where the path is too long for a unix socket.
// Named after the data dir, so a second deepcove on this machine cannot sweep the first one's sessions.
func ensureIPCDir(dataDir string) (string, error) {
	uid := os.Getuid()
	name := "deepcove-" + shortHash(dataDir)
	// The runtime directory first, then the temp one. A candidate we cannot make, or that turns out to
	// be someone else's, is passed over rather than fatal: the next one is just as good.
	var refused error
	for _, dir := range []string{runtimeDir(uid, name), filepath.Join(os.TempDir(), name)} {
		if err := os.MkdirAll(dir, ipcDirMode); err != nil {
			refused = errors.Join(refused, err)
			continue
		}
		if err := requirePrivateToUs(dir, uid); err != nil {
			refused = errors.Join(refused, err)
			continue
		}
		return dir, nil
	}
	return "", fmt.Errorf("no place to put deepcove's sockets: %w", refused)
}

func runtimeDir(uid int, name string) string {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return filepath.Join(base, name)
	}
	return filepath.Join("/run/user", strconv.Itoa(uid), name)
}

// One of these sockets hands out a session's credentials, and another is a shell, so the directory is
// the whole of their protection.
const ipcDirMode = 0o700

// requirePrivateToUs refuses a directory another user owns, and tightens one of ours that is open to
// others. In /tmp anyone can create the name first and wait for us to put sockets in it.
func requirePrivateToUs(path string, uid int) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot tell who owns %s", path)
	}
	if int(owner.Uid) != uid {
		return fmt.Errorf("%s belongs to uid %d, not to you, so deepcove's sockets are not going in it",
			path, owner.Uid)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return os.Chmod(path, ipcDirMode)
}
