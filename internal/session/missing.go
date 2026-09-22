package session

import (
	"context"
	"sort"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// A node can know about a runtime it cannot provide. The VM runtime needs a guest kernel on disk, so a
// machine without one offers only containers, and until now that machine looked like it simply did not
// support VMs: nothing registered, so nothing appeared anywhere.
//
// This is the third state. Registered means a session can land on it. Unavailable means it is registered
// but its backend is not answering right now. Missing means the driver was never built, and something
// has to be installed before it can be.
//
// The reason is a stable code rather than a sentence, so the UI owns the wording and can attach an
// action to it without a server change.
const (
	// MissingKernel: no guest kernel where the runtime looked for one.
	MissingKernel = "no_kernel"
	// MissingGuestInit: no guest init binary, the PID 1 every session VM boots.
	MissingGuestInit = "no_guest_init"
	// MissingHostBinaries: the host programs that run a VM are not downloaded yet.
	MissingHostBinaries = "no_host_binaries"
	// MissingTmux: tmux is not installed, and installing it needs a package manager and a password.
	MissingTmux = "no_tmux"
)

// RuntimeInstaller sets up a runtime this node is missing, reporting progress as it goes. On success the
// runtime is registered and can host sessions immediately, so nothing needs restarting.
//
// The session package does not know how to build a runtime; the CLI does, and hands one of these over.
type RuntimeInstaller func(ctx context.Context, report func(sandbox.PullProgress)) error

// MarkRuntimeMissing records a runtime this node knows about but cannot provide. Nothing can be created
// on it, so it is deliberately not registered; it is reported so the UI can explain the gap.
func (s *Service) MarkRuntimeMissing(name, reason string) {
	s.missingMu.Lock()
	defer s.missingMu.Unlock()
	if s.missingRuntimes == nil {
		s.missingRuntimes = make(map[string]string)
	}
	s.missingRuntimes[name] = reason
}

// MarkRuntimeReady drops a runtime from the missing list, once whatever it needed has been installed.
func (s *Service) MarkRuntimeReady(name string) {
	s.missingMu.Lock()
	defer s.missingMu.Unlock()
	delete(s.missingRuntimes, name)
}

// SetRuntimeInstaller records how to set up a runtime that is missing. Without one, the UI can explain
// the gap but not offer to close it.
func (s *Service) SetRuntimeInstaller(name string, install RuntimeInstaller) {
	s.missingMu.Lock()
	defer s.missingMu.Unlock()
	if s.runtimeInstallers == nil {
		s.runtimeInstallers = make(map[string]RuntimeInstaller)
	}
	s.runtimeInstallers[name] = install
}

// runtimeInstaller returns how to set up name, and whether this node knows how.
func (s *Service) runtimeInstaller(name string) (RuntimeInstaller, bool) {
	s.missingMu.Lock()
	defer s.missingMu.Unlock()
	install, ok := s.runtimeInstallers[name]
	return install, ok
}

// MissingRuntimes returns what this node cannot provide, keyed by runtime name.
func (s *Service) MissingRuntimes() map[string]string {
	s.missingMu.Lock()
	defer s.missingMu.Unlock()
	if len(s.missingRuntimes) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.missingRuntimes))
	for name, reason := range s.missingRuntimes {
		out[name] = reason
	}
	return out
}

// missingRuntimeNames lists them in a stable order, so a view built from them does not shuffle between
// requests the way a map range would.
func (s *Service) missingRuntimeNames() []string {
	s.missingMu.Lock()
	defer s.missingMu.Unlock()
	names := make([]string, 0, len(s.missingRuntimes))
	for name := range s.missingRuntimes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
