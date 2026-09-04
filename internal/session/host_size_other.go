//go:build !darwin && !linux

package session

// hostSize reports only what the standard library can tell us. Memory and free space stay zero, so the
// derived size falls back to the minimums rather than guessing at a platform we cannot measure.
func hostSize(string) hostResources {
	return hostResources{cores: usableCores()}
}
