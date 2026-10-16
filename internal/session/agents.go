package session

import (
	_ "embed"
	"fmt"
	"sort"

	"github.com/waltzofpearls/deepcove/internal/llmproxy"
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

//go:embed agents/codex_sandboxed_setup.sh
var codexSandboxedSetup string

//go:embed agents/codex_conversation.sh
var codexConversation string

//go:embed agents/codex_sandboxed_launch.sh
var codexSandboxedLaunch string

//go:embed agents/codex_sandboxless_launch.sh
var codexSandboxlessLaunch string

// agentsImage is the prebuilt guest image, with every agent already installed.
const agentsImage = "ghcr.io/waltzofpearls/deepcove-agents:latest"

// AgentSpec is one agent as this build knows it, and the public shape returned by GET /api/agents: the
// SPA's "new session" form reads it for the agent dropdown and to pre-populate the setup-script
// textarea.
//
// A runtime with a guest installs the agent inside it, and one without a guest installs it on the
// machine. The two need different commands, so each has its own half.
type AgentSpec struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	// LoginModes are the ways this agent may be logged in, safest first. Each vendor sets its own rules,
	// so a mode is listed only after checking that vendor's terms.
	LoginModes []LoginMode `json:"login_modes"`
	// LoginHint says how to log in inside a session, for the login picker.
	LoginHint string `json:"login_hint"`
	// APIKeyExample is what the vendor's API keys start with, for the key field in Settings.
	APIKeyExample string        `json:"api_key_example"`
	Sandboxed     SandboxedSpec `json:"sandboxed"`
	// Sandboxless stays on the node. It is a command to run on the machine, and the create form has
	// nothing to show from it: it says "not installed yet" from the preflight instead.
	Sandboxless SandboxlessSpec `json:"-"`
	// API is how the agent reaches its vendor when deepcove stands in between, for the API key and the
	// LLM proxy. Its login is kept per agent in package sharedlogin, which the guest reads too.
	API AgentAPI `json:"-"`
}

// AgentAPI is how one agent is pointed at the host instead of its vendor.
type AgentAPI struct {
	// Provider is where the host sends a session that uses the user's API key, and how the key goes on.
	Provider llmproxy.Provider
	// BaseURLEnv and TokenEnv point the agent at the host. The token carries the session id.
	BaseURLEnv string
	TokenEnv   string
	// ClearEnv is every variable the agent could take an address or a credential from. All of them are
	// cleared before each start, so a mode that is off is off whatever the machine left behind.
	ClearEnv []string
}

// LoginMode is one way an agent gets logged in.
type LoginMode string

const (
	// LoginInSession is the agent's own login flow, run inside the session. Every agent allows it.
	LoginInSession LoginMode = "in_session"
	// LoginShared is the user's own login, kept by deepcove and handed to each session that shares it.
	LoginShared LoginMode = "shared"
	// LoginWithAPIKey uses the user's own API key, which deepcove keeps and adds to each request.
	LoginWithAPIKey LoginMode = "api_key"
	// LoginWithLLMProxy sends requests to the node's LLM proxy, which holds the credential.
	LoginWithLLMProxy LoginMode = "llm_proxy"
)

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

// defaultAgent is the agent a machine is built for when no session picked one.
const defaultAgent = "claude"

var agents = map[string]AgentSpec{
	"claude": {
		Name:          "claude",
		DisplayName:   "Claude Code",
		LoginModes:    []LoginMode{LoginInSession, LoginShared, LoginWithAPIKey, LoginWithLLMProxy},
		LoginHint:     "Run /login in this session.",
		APIKeyExample: "sk-ant-…",
		Sandboxed: SandboxedSpec{
			DefaultImage:  "alpine:3.20",
			OfficialImage: agentsImage,
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
		API: AgentAPI{
			Provider:   llmproxy.Provider{URL: "https://api.anthropic.com", KeyHeader: "X-Api-Key"},
			BaseURLEnv: "ANTHROPIC_BASE_URL",
			TokenEnv:   "ANTHROPIC_AUTH_TOKEN",
			ClearEnv:   []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"},
		},
	},
	"codex": {
		Name:        "codex",
		DisplayName: "Codex",
		LoginModes:  []LoginMode{LoginInSession, LoginShared, LoginWithAPIKey},
		// The browser login cannot reach back into a VM, so the device code is the one that works.
		LoginHint:     "Sign in with a device code in this session.",
		APIKeyExample: "sk-proj-…",
		Sandboxed: SandboxedSpec{
			DefaultImage:  "alpine:3.20",
			OfficialImage: agentsImage,
			Setup:         codexSandboxedSetup,
			LaunchCmd:     codexConversation + codexSandboxedLaunch,
			Env: map[string]string{
				"LANG":   "C.UTF-8",
				"LC_ALL": "C.UTF-8",
			},
		},
		Sandboxless: SandboxlessSpec{
			Binary: "codex",
			// Same as Claude: the installer puts the binary in $HOME/.local/bin of the shared agent home.
			Install:   "curl -fsSL https://chatgpt.com/codex/install.sh | sh",
			LaunchCmd: codexConversation + codexSandboxlessLaunch,
		},
		API: AgentAPI{
			Provider: llmproxy.Provider{URL: "https://api.openai.com", KeyHeader: "Authorization"},
			// Codex has no variables for these, so its launch script turns them into a model provider.
			BaseURLEnv: "DEEPCOVE_LLM_BASE_URL",
			TokenEnv:   "DEEPCOVE_LLM_TOKEN",
			ClearEnv: []string{
				"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN",
				"DEEPCOVE_LLM_BASE_URL", "DEEPCOVE_LLM_TOKEN",
			},
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
