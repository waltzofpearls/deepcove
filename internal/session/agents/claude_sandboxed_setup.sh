apk add --no-cache curl bash git tmux libgcc libstdc++ ripgrep
# Check the install path as well as PATH: with a persistent home the binary is already there from a
# previous boot, but $HOME/.local/bin is not on PATH this early, so a PATH-only test reinstalls it.
# Downloaded before it runs: piped, a failed download hands bash nothing to run, which bash counts as success.
if ! command -v claude >/dev/null 2>&1 && [ ! -x "$HOME/.local/bin/claude" ]; then
  curl -fsSL https://claude.ai/install.sh -o /tmp/claude-install.sh
  bash /tmp/claude-install.sh
fi
[ -x /root/.local/bin/claude ] && ln -sf /root/.local/bin/claude /usr/local/bin/claude
# Setup fails here when Claude is missing, rather than leave the agent window failing to start it.
command -v claude >/dev/null
