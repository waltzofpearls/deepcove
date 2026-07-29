package applevf

import (
	"crypto/rand"
	"encoding/hex"
)

// newHandleID mints an opaque sandbox handle. Random rather than derived from the session, because the
// interface treats a handle as a token whose contents callers must not interpret.
func newHandleID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "applevf-" + hex.EncodeToString(b[:])
}
