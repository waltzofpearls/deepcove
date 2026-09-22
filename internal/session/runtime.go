package session

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/waltzofpearls/deepcove/internal/sandbox"
)

// Runtime names. These are persisted on the session row and sent over the wire, so treat them as a
// stable vocabulary rather than internal identifiers.
const (
	// RuntimeDocker runs each session in a container sharing the host kernel. The default everywhere.
	RuntimeDocker = "docker"
	// RuntimeAppleVF runs each session in its own Linux VM via Apple Virtualization.framework. macOS
	// only, and only when the guest kernel and init are present.
	RuntimeAppleVF = "applevf"
	// RuntimeCloudHV runs each session in its own Linux VM via Cloud Hypervisor. Linux only, and only
	// when KVM is usable and the VM artifacts are present.
	RuntimeCloudHV = "cloudhv"
	// RuntimeSandboxless runs each session on the machine itself, with no VM and no container. For a
	// machine that is already the boundary. Needs tmux, which it cannot install.
	RuntimeSandboxless = "sandboxless"
)

// SandboxesOutliveDeepcove reports whether this runtime's sandboxes are still there once deepcove
// exits. A VM runs inside deepcove, as threads of this process, so its sandboxes go when it does.
func SandboxesOutliveDeepcove(runtime string) bool {
	switch runtime {
	case RuntimeSandboxless, RuntimeDocker:
		return true
	}
	return false
}

// vmRuntimes are the runtime names that boot a VM, one per hypervisor. A node offers at most one of
// them, because a host has one hypervisor worth using.
var vmRuntimes = []string{RuntimeAppleVF, RuntimeCloudHV}

// runtimeBootsVMs reports whether a runtime gives each machine its own kernel. Docker never does.
func runtimeBootsVMs(name string) bool {
	return slices.Contains(vmRuntimes, name)
}

// ErrUnknownRuntime means a session names a runtime this node does not offer. That is a real situation
// rather than a bug: a session created on a Mac with the VM runtime is unusable on a node without it, and
// the user needs to be told that rather than shown a confusing driver error.
type ErrUnknownRuntime struct {
	Runtime string
}

func (e ErrUnknownRuntime) Error() string {
	return fmt.Sprintf("session: this node does not provide the %q runtime", e.Runtime)
}

// RegisterRuntime adds a runtime under name. The runtime passed to NewService is already registered as
// the default, so this is for the additional ones a node happens to support.
func (s *Service) RegisterRuntime(name string, sb sandbox.Sandbox) {
	if sb == nil {
		return
	}
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	s.runtimes[name] = sb
}

// lookupRuntime resolves one driver by name.
func (s *Service) lookupRuntime(name string) (sandbox.Sandbox, bool) {
	s.runtimesMu.RLock()
	defer s.runtimesMu.RUnlock()
	sb, ok := s.runtimes[name]
	return sb, ok
}

// registeredRuntimes copies the drivers, so a caller can walk them without holding the lock through
// whatever it does with each one.
func (s *Service) registeredRuntimes() map[string]sandbox.Sandbox {
	s.runtimesMu.RLock()
	defer s.runtimesMu.RUnlock()
	return maps.Clone(s.runtimes)
}

// SelectDefaultRuntime decides which runtime a create request gets when it names none.
//
// An empty preference takes the first runtime this node actually provides, in runtimeOrder, which puts
// the strongest isolation first. That is the whole policy: no runtime is named here, so adding one only
// means placing it in that order. A non-empty preference (DEEPCOVE_DEFAULT_RUNTIME) must be a runtime
// this node provides, and is reported as an error rather than silently ignored.
func (s *Service) SelectDefaultRuntime(ctx context.Context, preferred string) error {
	if preferred != "" {
		if _, ok := s.lookupRuntime(preferred); !ok {
			return ErrUnknownRuntime{Runtime: preferred}
		}
		s.setDefaultRuntime(preferred)
		return nil
	}
	available := s.Runtimes()
	if len(available) == 0 {
		return fmt.Errorf("session: this node provides no runtimes")
	}

	// Prefer one that can actually host a session today. Registering a driver only says this node knows
	// how to talk to that backend; Docker in particular can be installed but not running, and defaulting
	// to it would greet the user with a failure they did not choose.
	unavailable := s.unavailableRuntimes(ctx)
	for _, name := range available {
		if _, dead := unavailable[name]; !dead {
			s.defaultRuntime = name
			return nil
		}
	}
	// Nothing is reachable. Still pick one so the node has a coherent default to report; creating a
	// session is refused separately, with the reason, rather than silently attempted.
	s.defaultRuntime = available[0]
	return nil
}

// SetDefaultRuntime changes which runtime a create request gets when it does not ask for one. It
// refuses a runtime this node does not provide, so a misconfigured default degrades to the existing one
// rather than failing every create.
func (s *Service) SetDefaultRuntime(name string) error {
	if _, ok := s.runtimes[name]; !ok {
		return ErrUnknownRuntime{Runtime: name}
	}
	s.defaultRuntime = name
	return nil
}

// pickVMRuntime is the runtime a workspace VM runs on: the one asked for, else the node default when it
// boots VMs, else this node's VM runtime.
//
// No caller sends a runtime today, so reading the default alone refuses every request on a node whose
// default is a container runtime, even when it has a VM runtime registered.
func (s *Service) pickVMRuntime(requested string) (string, error) {
	if requested != "" {
		if !runtimeBootsVMs(requested) {
			return "", fmt.Errorf("%w: a workspace VM needs a VM runtime, and %s is not one",
				ErrInvalidInput, requested)
		}
		return requested, nil
	}
	if runtimeBootsVMs(s.defaultRuntime) {
		return s.defaultRuntime, nil
	}
	for _, name := range s.Runtimes() {
		if runtimeBootsVMs(name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("%w: a workspace VM needs a VM runtime, and this node has none", ErrInvalidInput)
}

// runtimeOrder is the order runtimes are offered in, strongest isolation first. Map iteration is random,
// so without this the picker reshuffles between restarts and there is no stable "first" option to land
// on. A runtime not named here sorts last, alphabetically, rather than disappearing.
var runtimeOrder = []string{RuntimeAppleVF, RuntimeCloudHV, RuntimeDocker, RuntimeSandboxless}

// Runtimes lists the runtimes this node provides, so the UI can offer them and the create path can
// validate a requested one. The order is deliberate and stable; see runtimeOrder.
func (s *Service) Runtimes() []string {
	registered := s.registeredRuntimes()
	out := make([]string, 0, len(registered))
	for name := range registered {
		out = append(out, name)
	}
	slices.SortFunc(out, func(a, b string) int {
		ra, rb := slices.Index(runtimeOrder, a), slices.Index(runtimeOrder, b)
		if ra < 0 {
			ra = len(runtimeOrder)
		}
		if rb < 0 {
			rb = len(runtimeOrder)
		}
		if ra != rb {
			return ra - rb
		}
		return strings.Compare(a, b)
	})
	return out
}

// DefaultRuntime is what a create request gets when it does not ask for one.
func (s *Service) DefaultRuntime() string {
	s.runtimesMu.RLock()
	defer s.runtimesMu.RUnlock()
	return s.defaultRuntime
}

func (s *Service) setDefaultRuntime(name string) {
	s.runtimesMu.Lock()
	defer s.runtimesMu.Unlock()
	s.defaultRuntime = name
}

// runtimeByName resolves a runtime by name, falling back to the default when name is empty. An empty name
// covers both a create request that did not choose one and a session row written before the runtime
// column existed.
func (s *Service) runtimeByName(name string) (sandbox.Sandbox, error) {
	if name == "" {
		name = s.DefaultRuntime()
	}
	sb, ok := s.lookupRuntime(name)
	if !ok {
		return nil, ErrUnknownRuntime{Runtime: name}
	}
	return sb, nil
}

// runtimeFor resolves the driver hosting an existing session. Every operation on a live session goes
// through here, because a node can offer several runtimes and the session must go back to the one that
// created it.
func (s *Service) runtimeFor(sess *Session) (sandbox.Sandbox, error) {
	return s.runtimeByName(sess.Runtime)
}

// agentProvisionerFor is how a session asks its runtime about the agent: setup progress, what starts it,
// and where it works. Every runtime answers, so a driver that does not is a build this node cannot use.
func (s *Service) agentProvisionerFor(sess *Session) (sandbox.AgentProvisioner, error) {
	runtime, err := s.runtimeFor(sess)
	if err != nil {
		return nil, err
	}
	provisioner, ok := runtime.(sandbox.AgentProvisioner)
	if !ok {
		return nil, fmt.Errorf("session: the %s runtime cannot answer for its agent", sess.Runtime)
	}
	return provisioner, nil
}

// ErrRuntimeUnavailable means the runtime exists on this node but cannot host a session right now, which
// is a different situation from a runtime this node has never heard of: the user does not need to pick
// something else, they need to start the backend.
type ErrRuntimeUnavailable struct {
	Runtime string
	Reason  string
}

func (e ErrRuntimeUnavailable) Error() string { return e.Reason }

// runtimeUnavailable reports why one runtime cannot host a session, or "" when it can.
//
// Probes only the runtime asked about. Checking a single runtime by probing all of them would make every
// create wait on backends it is not going to use.
func (s *Service) runtimeUnavailable(ctx context.Context, name string) string {
	if name == "" {
		name = s.DefaultRuntime()
	}
	driver, ok := s.lookupRuntime(name)
	if !ok {
		return "" // an unknown runtime is reported by runtimeByName, not here
	}
	pinger, ok := driver.(sandbox.Pinger)
	if !ok {
		return "" // cannot report liveness, so assume usable rather than block on a signal we lack
	}
	pingCtx, cancel := context.WithTimeout(ctx, preflightPingTimeout)
	defer cancel()
	if _, err := pinger.Ping(pingCtx); err != nil {
		return unavailableReason(name)
	}
	return ""
}
