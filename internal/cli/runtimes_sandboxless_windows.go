package cli

// newSandboxlessRuntime has nothing to build here. It runs the agent through a unix pty and a tmux
// server, and Windows has neither.
func newSandboxlessRuntime(runtimeDeps) runtimeEntry {
	return runtimeEntry{}
}
