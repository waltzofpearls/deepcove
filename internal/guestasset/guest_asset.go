// Package guestasset carries the guest init binary inside deepcove and writes it to disk when the VM
// runtime asks for it.
//
// The guest init is the PID 1 every session VM boots. Keeping it in the binary means a downloaded
// release can run VMs with nothing else to install, and the init can never be older than the binary.
package guestasset

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed deepcove-guest-init
var binary []byte

// Name is what the init is called on disk, matching what the Makefile builds.
const Name = "deepcove-guest-init"

// Available reports whether a real init was embedded. A tree that has not run `make guest-init` embeds
// an empty placeholder, and the runtime then reports itself as needing setup rather than booting a VM
// with no init.
func Available() bool { return len(binary) > 0 }

// Install writes the init into dir and returns its path.
func Install(dir string) (string, error) { return install(dir, binary) }

// install is the testable half: it never reads the embedded bytes, so a test does not depend on whether
// this tree happens to have built a real init.
//
// Rewrites only when the content differs, so a restart that changed nothing leaves the file alone.
// Executable, because the VM's kernel runs it directly.
func install(dir string, content []byte) (string, error) {
	if len(content) == 0 {
		return "", fmt.Errorf("guestasset: no guest init was built into this binary")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("guestasset: %w", err)
	}

	path := filepath.Join(dir, Name)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return path, nil
	}

	// Write beside the target and rename, so a crash mid write cannot leave a truncated init that the
	// next boot would try to run.
	tmp, err := os.CreateTemp(dir, Name+".*")
	if err != nil {
		return "", fmt.Errorf("guestasset: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("guestasset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("guestasset: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", fmt.Errorf("guestasset: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("guestasset: %w", err)
	}
	return path, nil
}
