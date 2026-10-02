//go:build !darwin && !linux

package service

import (
	"fmt"
	"runtime"
)

// New reports that this platform has no service support yet, rather than pretending to install one.
func New(_, _ string) (Manager, error) {
	return nil, fmt.Errorf("service: deepcove cannot install itself as a service on %s yet", runtime.GOOS)
}

// ProtectedFolders is empty: no user service here.
func ProtectedFolders(string) []string { return nil }
