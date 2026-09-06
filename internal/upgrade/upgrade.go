// Package upgrade replaces the running deepcove binary with a release published on GitHub.
package upgrade

import (
	"fmt"
	"os"
)

// AssetName is what the release workflow called this platform's archive.
func AssetName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "darwin" || goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("deepcove_%s_%s-%s.%s", version, goos, goarch, ext)
}

// BinaryName is what the archive calls the binary inside itself.
func BinaryName(goos string) string {
	if goos == "windows" {
		return "deepcove.exe"
	}
	return "deepcove"
}

// Download writes the named asset into dir and checks it against the release's own SHA256SUMS. Nothing
// is returned unless the sum matches. The caller removes the file.
//
// dir is where the binary lives, so the last step of an upgrade is a rename within one filesystem.
// report is called as bytes arrive and may be nil. Its total is zero when the server did not say how
// big the file is.

// Replace puts newPath at target, keeping the permissions target already had.
//
// The old binary is moved aside first, because Windows refuses to overwrite a running executable, and
// is put back if the second rename fails.
func Replace(target, newPath string) error {
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	if err := os.Chmod(newPath, info.Mode().Perm()); err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}
	old := target + ".old"
	_ = os.Remove(old)
	if err := os.Rename(target, old); err != nil {
		return fmt.Errorf("upgrade: could not move %s aside: %w", target, err)
	}
	if err := os.Rename(newPath, target); err != nil {
		_ = os.Rename(old, target)
		return fmt.Errorf("upgrade: could not put the new binary at %s: %w", target, err)
	}
	// Windows holds this open until the process exits, so it can only be attempted.
	_ = os.Remove(old)
	return nil
}

// CanWrite reports whether a new file can be created in dir. It writes rather than reads the mode,
// because ownership, ACLs and read only mounts all decide this and only a real write knows.
func CanWrite(dir string) bool {
	file, err := os.CreateTemp(dir, ".deepcove-write-*")
	if err != nil {
		return false
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	return true
}
