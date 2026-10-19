apk add --no-cache curl bash git tmux ripgrep procps-ng bubblewrap
# Codex's background server reads processes with the full ps, which busybox lacks. bubblewrap is what
# Codex's own sandbox runs its commands in. Same install path check as Claude's, for a persistent home.
# Downloaded before it runs: piped, a failed download hands sh nothing to run, which sh counts as success.
if ! command -v codex >/dev/null 2>&1 && [ ! -x "$HOME/.local/bin/codex" ]; then
  curl -fsSL https://chatgpt.com/codex/install.sh -o /tmp/codex-install.sh
  # Its installer asks to start Codex once done, and setup has nobody to answer.
  CODEX_NON_INTERACTIVE=1 sh /tmp/codex-install.sh
fi
[ -x /root/.local/bin/codex ] && ln -sf /root/.local/bin/codex /usr/local/bin/codex
# Setup fails here when Codex is missing, rather than leave the agent window failing to start it.
command -v codex >/dev/null
