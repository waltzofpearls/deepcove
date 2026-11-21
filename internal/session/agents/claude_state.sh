# Claude's session file says busy, waiting or idle, but not whether a turn finished or failed. These hooks
# mark both, and the next turn clears them. They come in with --settings, so the user's settings stay as
# they are.
claude_settings="$state_dir/claude-settings.json"
cat >"$claude_settings" <<'EOF'
{"hooks": {
  "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "rm -f \"$DEEPCOVE_AGENT_STATE_DIR/done\" \"$DEEPCOVE_AGENT_STATE_DIR/error\""}]}],
  "Stop": [{"hooks": [{"type": "command", "command": "touch \"$DEEPCOVE_AGENT_STATE_DIR/done\""}]}],
  "StopFailure": [{"hooks": [{"type": "command", "command": "cat >\"$DEEPCOVE_AGENT_STATE_DIR/error\""}]}]
}}
EOF
