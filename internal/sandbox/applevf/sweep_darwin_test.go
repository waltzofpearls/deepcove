//go:build darwin

package applevf

import (
	"path/filepath"
	"testing"
)

func TestPIDFileLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vm.pid")
	pidLock, err := holdPIDFile(path)
	if err != nil {
		t.Fatalf("holdPIDFile: %v", err)
	}
	if !pidFileLocked(path) {
		t.Error("a held pid file reads as not locked")
	}
	_ = pidLock.Close()
	if pidFileLocked(path) {
		t.Error("a pid file whose holder is gone still reads as locked")
	}
}
