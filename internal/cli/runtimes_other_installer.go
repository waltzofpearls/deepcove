//go:build !darwin && !linux

package cli

// registerVMInstaller is a no-op here: there is no VM runtime to set up, so the UI is never offered a
// button for one.
func registerVMInstaller(runtimeDeps, *runtimeSet) {
}
