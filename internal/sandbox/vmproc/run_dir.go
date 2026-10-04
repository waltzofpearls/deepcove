package vmproc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// NewRunDir makes a private folder under root for one VM's sockets. Short and random, because a socket
// path has a length limit.
func NewRunDir(root string) (string, error) {
	var id [4]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	dir := filepath.Join(root, hex.EncodeToString(id[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("vmproc: %w", err)
	}
	return dir, nil
}
