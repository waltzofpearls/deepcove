//go:build !darwin && !linux

package session

// hostMemoryKeptBackMB is unused here, since the host memory is unknown.
const hostMemoryKeptBackMB = 0

// hostMemoryMB is unknown here, so the size cards say nothing about how many fit.
func hostMemoryMB() int64 { return 0 }
