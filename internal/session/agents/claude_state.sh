# Claude's session file says busy, waiting or idle, but not whether a turn finished or failed. These hooks
# mark both, and the next turn clears them. They come in with --settings, so the user's settings stay as
# they are.
claude_settings="$state_dir/claude-settings.json"
cat >"$claude_settings" <<EOF
{"hooks": {
  "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "rm -f '$state_dir/done' '$state_dir/error'"}]}],
  "Stop": [{"hooks": [{"type": "command", "command": "touch '$state_dir/done'"}]}],
  "StopFailure": [{"hooks": [{"type": "command", "command": "cat >'$state_dir/error'"}]}]
}}
EOF
