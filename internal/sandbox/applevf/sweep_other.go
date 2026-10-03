//go:build !darwin

package applevf

// pidFileLocked finds nothing here: `vm run` only exists on macOS.
func pidFileLocked(string) bool { return false }
