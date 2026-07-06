package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// socketPath returns the per-OS path of the local control Unix socket for the given data dir. The name
// is derived from the data dir so multiple `deepcove start` instances on one machine (each with its own
// DATA_DIR) get distinct sockets instead of clobbering each other's, while the path stays short (Unix
// socket paths have a low length limit) by living in the per-user tmp dir. The server and the CLI both
// resolve the same data dir, so they agree on the path without extra configuration. The directory is
// user private, so the socket inherits owner-only access; on Unix we additionally chmod the socket 0600.
func socketPath(dataDir string) string {
	name := "deepcove-" + shortHash(dataDir) + ".sock"
	switch runtime.GOOS {
	case "windows":
		// %LOCALAPPDATA%\deepcove is per-user, ACL'd to the owner by Windows (os.Chmod can't
		// express POSIX perms there, so the dir is the boundary).
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = os.TempDir()
		}
		return filepath.Join(dir, "deepcove", name)
	case "darwin":
		// $TMPDIR is per-user on macOS.
		return filepath.Join(os.TempDir(), name)
	default:
		// Linux and other Unix: prefer $XDG_RUNTIME_DIR (per-user tmpfs), else the temp dir.
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			return filepath.Join(dir, name)
		}
		return filepath.Join(os.TempDir(), name)
	}
}

// shortHash returns the first 8 hex chars of sha256(s), enough to keep per-data-dir socket names
// distinct without bloating the path length.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// socketAvailable reports whether the control socket exists; i.e. a current `deepcove start` is
// serving its Unix-socket front door. When false the CLI falls back to the local TCP address
// (older server, or dev).
func socketAvailable(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// socketHTTPClient dials the local control socket regardless of the request URL's host (use http://unix
// as the base).
func socketHTTPClient(path string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
}

// listenUnixSocket creates the control socket (parent dir 0700, removing any stale socket from a
// crashed run) and returns an http.Server + listener serving the given handler. Owner-only via
// chmod 0600 on Unix; on Windows the per-user dir ACL is the boundary.
func listenUnixSocket(path string, handler http.Handler) (*http.Server, net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = ln.Close()
			return nil, nil, err
		}
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	return srv, ln, nil
}

// localAllowedHosts derives the LocalGate Host allowlist from the TCP bind address. Loopback is
// always allowed by the gate; a wildcard bind (0.0.0.0 / ::) contributes nothing (only loopback
// is trusted), while an explicit host is added so binding to a specific name/IP still works.
func localAllowedHosts(addr string) []string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		return nil
	}
	return []string{host}
}
