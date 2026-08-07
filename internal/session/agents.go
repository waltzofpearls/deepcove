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
# Check the install path as well as PATH: with a persistent home the binary is already there from a
# previous boot, but $HOME/.local/bin is not on PATH this early, so a PATH-only test reinstalls it.
command -v claude >/dev/null 2>&1 || [ -x "$HOME/.local/bin/claude" ] || curl -fsSL https://claude.ai/install.sh | bash
[ -x /root/.local/bin/claude ] && ln -sf /root/.local/bin/claude /usr/local/bin/claude`,
		LaunchCmd: `export PATH="$HOME/.local/bin:$PATH"
# Pick up the previous conversation when there is one, decided once per boot.
#
# A container keeps its agent process across a deepcove restart, so it never notices. A VM does not: it
# is rebuilt from scratch, and the agent would start blank even though its history is sitting on the
# session's persistent home. --continue resumes the most recent conversation in the working directory.
#
# Cleared after the agent exits, so a resume that cannot work falls back to a fresh session on the next
# attempt instead of failing the same way forever.
resume=""
if ls "$HOME"/.claude/projects/*/*.jsonl >/dev/null 2>&1; then
  resume="--continue"
fi
# Consecutive out-of-memory kills. An OOM is deterministic: the session's memory does not grow between
# attempts, so retrying forever just burns CPU and scrolls the explanation out of view. Reset on any
# other exit, so an unlucky one-off spike still gets retried.
ooms=0
while true; do
  cd "${DEEPCOVE_WORKDIR:-/workspace}"
  claude $resume
  ec=$?
  resume=""
  echo ""
  # 137 is SIGKILL, and in a VM with no swap that is almost always the kernel reclaiming memory. Say so
  # instead of printing a bare number: "code 137" sends you hunting through deepcove, when the answer is
  # that the session needs more memory. Checking dmesg keeps it honest, since SIGKILL has other sources.
  # Any OOM kill on record is enough: this VM runs one agent, so if the kernel reclaimed memory and our
  # process just took a SIGKILL, that is the same event. Matching on the process name looked tighter but
  # is fragile, because the agent re-execs its own bundled runtime under a different name.
  if [ "$ec" -eq 137 ] && dmesg 2>/dev/null | grep -qi "out of memory: killed process"; then
    ooms=$((ooms + 1))
    echo "claude ran out of memory and the kernel killed it."
    echo "This session has $(awk '/MemTotal/{printf "%d", $2/1024}' /proc/meminfo 2>/dev/null)MiB of RAM."
    dmesg 2>/dev/null | grep -i "out of memory: killed process" | tail -1
    if [ "$ooms" -ge 3 ]; then
      echo ""
      echo "Stopping after $ooms out-of-memory kills. Restarting cannot help: this session's memory is"
      echo "fixed at creation, so it would hit the same limit again."
      echo "Create a new session with more memory. Dropping to a shell so you can look around."
      # exec, not exit: leaving the window on a shell keeps the messages above readable and the pane
      # usable. Exiting would close the window and take the explanation with it.
      exec /bin/sh
    fi
    echo "Retrying in 3s in case it was a one-off spike ($ooms of 3)."
  else
    ooms=0
    echo "claude exited (code $ec); restarting in 3s. Switch to SSH mode for a shell"
  fi
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
