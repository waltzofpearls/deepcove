package session

import (
	_ "embed"
	"fmt"
	"sort"
)

// The agent scripts are .sh files so they read as shell and shellcheck can see them. Each is a fragment
// the runtime wraps, so none of them carries a shebang.
//
//go:embed agents/claude_sandboxed_setup.sh
var claudeSandboxedSetup string

//go:embed agents/claude_sandboxed_launch.sh
var claudeSandboxedLaunch string

//go:embed agents/claude_sandboxless_launch.sh
var claudeSandboxlessLaunch string

// AgentSpec is one agent as this build knows it, and the public shape returned by GET /api/agents: the
// SPA's "new session" form reads it for the agent dropdown and to pre-populate the setup-script
// textarea. v1 returns Claude only; codex/gemini/opencode are deferred until each is verified
// end-to-end (see project_agent_works_deferred.md).
//
// A runtime with a guest installs the agent inside it, and one without a guest installs it on the
// machine. The two need different commands, so each has its own half.
type AgentSpec struct {
	Name        string        `json:"name"`
	DisplayName string        `json:"display_name"`
	Sandboxed   SandboxedSpec `json:"sandboxed"`
	// Sandboxless stays on the node. It is a command to run on the machine, and the create form has
	// nothing to show from it: it says "not installed yet" from the preflight instead.
	Sandboxless SandboxlessSpec `json:"-"`
}

// SandboxedSpec is how this agent is built into a guest and started there.
type SandboxedSpec struct {
	DefaultImage  string `json:"default_image"`
	OfficialImage string `json:"official_image"`
	// Setup must be idempotent (`command -v <bin>` before installing); it runs every boot.
	Setup string `json:"setup"`
	// LaunchCmd runs in tmux window `main` and restarts the agent when it exits. Not sent out: the
	// entrypoint writes it into the guest, and nothing outside this node reads it.
	LaunchCmd string `json:"-"`
	// Env is what the agent needs inside that image, which is about the image and not about the agent.
	Env map[string]string `json:"env"`
}

// SandboxlessSpec is how this agent is put on the machine itself and started there. No package manager
// and no root: the install has to land under the home it is given.
type SandboxlessSpec struct {
	// Binary is what to look for on PATH and in the agent home.
	Binary string `json:"binary"`
	// Install runs only when there is no binary, with HOME set to the shared agent home.
	Install string `json:"install"`
	// LaunchCmd is written to a script and runs the agent at $DEEPCOVE_AGENT, the path already resolved.
	LaunchCmd string `json:"launch_cmd"`
	// Env is what the agent needs on the machine. Empty means it takes the machine as it finds it,
	// which is the point of this runtime.
	Env map[string]string `json:"env"`
}

func allAgents() []AgentSpec {
	out := make([]AgentSpec, 0, len(agents))
	for _, a := range agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// defaultAgent is the agent a machine is built for when no session picked one. v1 ships Claude only,
// so the shared machine is a Claude machine.
const defaultAgent = "claude"

var agents = map[string]AgentSpec{
	"claude": {
		Name:        "claude",
		DisplayName: "Claude Code",
		Sandboxed: SandboxedSpec{
			DefaultImage:  "alpine:3.20",
			OfficialImage: "ghcr.io/waltzofpearls/deepcove-claude:latest",
			Setup:         claudeSandboxedSetup,
			LaunchCmd:     claudeSandboxedLaunch,
			Env: map[string]string{
				// Claude's bundled ripgrep is glibc-linked and segfaults on Alpine musl.
				"USE_BUILTIN_RIPGREP": "0",
				// Alpine ships no locale; without UTF-8 tmux substitutes non-ASCII with `_`
				// when sending to clients. `tmux -u` in attach.go is the actual fix; this
				// just keeps less/vim/git-pager from doing similar things.
				"LANG":   "C.UTF-8",
				"LC_ALL": "C.UTF-8",
			},
		},
		Sandboxless: SandboxlessSpec{
			Binary: "claude",
			// The official installer puts the binary in $HOME/.local/bin, and HOME is the shared agent
			// home, so nothing outside deepcove's data directory is written.
			Install:   "curl -fsSL https://claude.ai/install.sh | bash",
			LaunchCmd: claudeSandboxlessLaunch,
		},
	},
}

func resolveAgent(name string) (AgentSpec, error) {
	a, ok := agents[name]
	if !ok {
		known := make([]string, 0, len(agents))
		for n := range agents {
			known = append(known, n)
		}
		sort.Strings(known)
		return AgentSpec{}, fmt.Errorf("unknown agent %q (known: %v)", name, known)
	}
	return a, nil
}
