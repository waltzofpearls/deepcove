//go:build !amd64 && !arm64

package guestasset

// No guest init for this architecture, so the VM runtime reports that it needs setup.
var binary []byte
