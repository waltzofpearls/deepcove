package session

import (
	"fmt"
	"sort"
)

// AgentInfo is the public shape returned by GET /api/agents: the SPA's "new session"
// form reads it for the agent dropdown and to pre-populate the setup-script textarea.
// v1 returns Claude only; codex/gemini/opencode are deferred until each is verified
// end-to-end (see project_agent_works_deferred.md).
type AgentInfo struct {
	Name          string            `json:"name"`
	DisplayName   string            `json:"display_name"`
	DefaultImage  string            `json:"default_image"`
	OfficialImage string            `json:"official_image"`
	DefaultSetup  string            `json:"default_setup"`
	LaunchCmd     string            `json:"launch_cmd"`
	Env           map[string]string `json:"env"`
}

func Agents() []AgentInfo {
	out := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DefaultSetup must be idempotent (`command -v <bin>` before installing); runs every
// container boot. LaunchCmd runs in tmux window `main` and auto-restarts on agent exit;
// SSH mode is the shell escape hatch.
var agents = map[string]AgentInfo{
	"claude": {
		Name:          "claude",
		DisplayName:   "Claude Code",
		DefaultImage:  "alpine:3.20",
		OfficialImage: "ghcr.io/waltzofpearls/deepcove-claude:latest",
		DefaultSetup: `apk add --no-cache curl bash tmux libgcc libstdc++ ripgrep
command -v claude >/dev/null 2>&1 || curl -fsSL https://claude.ai/install.sh | bash
[ -x /root/.local/bin/claude ] && ln -sf /root/.local/bin/claude /usr/local/bin/claude`,
		LaunchCmd: `export PATH="$HOME/.local/bin:$PATH"
while true; do
  cd "${DEEPCOVE_WORKDIR:-/workspace}"
  claude
  ec=$?
  echo ""
  echo "claude exited (code $ec); restarting in 3s — switch to SSH mode for a shell"
  sleep 3
done`,
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
}

func resolveAgent(name string) (AgentInfo, error) {
	a, ok := agents[name]
	if !ok {
		known := make([]string, 0, len(agents))
		for n := range agents {
			known = append(known, n)
		}
		sort.Strings(known)
		return AgentInfo{}, fmt.Errorf("unknown agent %q (known: %v)", name, known)
	}
	return a, nil
}
