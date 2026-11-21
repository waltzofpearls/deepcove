# Prints what one session's agent is doing: the machine's clock first, then whatever the agent left in
# the folder agent_state.sh makes. $1 is the session id, $2 the agent state source, and $3 the agent's
# process name. Times are seconds since 1970, and date -r reads a file's time on GNU, busybox and macOS
# alike.
state_dir="${TMPDIR:-/tmp}/deepcove-$1"
echo "now $(date +%s)"
case $2 in
claude_session_file)
	# Claude keeps a session file per process, named after its pid. One left by a process that died still
	# says what it said last, so only a live one counts.
	for session_file in "$HOME"/.claude/sessions/*.json; do
		grep -q "\"sessionId\":\"$1\"" "$session_file" 2>/dev/null || continue
		kill -0 "$(basename "$session_file" .json)" 2>/dev/null || continue
		echo "session_file $(cat "$session_file")"
	done
	[ -f "$state_dir/done" ] && echo "done $(date -r "$state_dir/done" +%s)"
	[ -f "$state_dir/error" ] && echo "error $(tr '\n' ' ' <"$state_dir/error")"
	;;
escape_sequence)
	[ -f "$state_dir/escape_sequence" ] && echo "escape_sequence $(date -r "$state_dir/escape_sequence" +%s) $(cat "$state_dir/escape_sequence")"
	# The escape sequence stays on what it last said when the agent dies, so it counts only while the
	# agent runs.
	pid=$(cat "$state_dir/pid" 2>/dev/null)
	[ -n "$pid" ] && pgrep -P "$pid" "$3" >/dev/null && echo "alive"
	;;
esac
exit 0
