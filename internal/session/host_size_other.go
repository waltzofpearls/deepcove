//go:build !darwin && !linux

package session

// hostMemoryMB is unknown here, so the size cards say nothing about how many fit.
func hostMemoryMB() int64 { return 0 }
