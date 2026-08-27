//go:build !darwin

package session

import "runtime"

// usableCores counts the cores a VM can use. Only Apple Silicon splits them into fast and slow.
func usableCores() int { return runtime.NumCPU() }
