package vm

import (
	"crypto/rand"
	"encoding/hex"
)

// handlePrefix starts every handle this driver mints. Console files and the sweep that clears them are
// both named from it, so they cannot drift apart.
const handlePrefix = "vm-"

// newHandleID mints an opaque sandbox handle. Random rather than derived from the session, because the
// interface treats a handle as a token whose contents callers must not interpret.
func newHandleID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return handlePrefix + hex.EncodeToString(b[:])
}
