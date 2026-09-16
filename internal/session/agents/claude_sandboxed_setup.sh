apk add --no-cache curl bash git tmux libgcc libstdc++ ripgrep
# Check the install path as well as PATH: with a persistent home the binary is already there from a
# previous boot, but $HOME/.local/bin is not on PATH this early, so a PATH-only test reinstalls it.
command -v claude >/dev/null 2>&1 || [ -x "$HOME/.local/bin/claude" ] || curl -fsSL https://claude.ai/install.sh | bash
[ -x /root/.local/bin/claude ] && ln -sf /root/.local/bin/claude /usr/local/bin/claude
