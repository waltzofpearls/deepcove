# Prints what one session's agent is doing: the machine's clock first, then whatever the agent left in
# the folder agent_state.sh makes. $1 is the session id, $2 the agent state source, and $3 the agent's
# process name. Times are seconds since 1970, and date -r reads a file's time on GNU, busybox and macOS
# alike.
state_dir="${DEEPCOVE_AGENT_STATE_DIR:-/tmp/deepcove-$1}"
pid=$(cat "$state_dir/pid" 2>/dev/null)
echo "now $(date +%s)"
case $2 in
claude_session_file)
	# Claude keeps a session file named after its pid, and runs as a child of the pane's own process. So
	# only this session's file is read, and only while its Claude is alive.
	for child in $(pgrep -P "$pid" 2>/dev/null); do
		session_file="$HOME/.claude/sessions/$child.json"
		[ -f "$session_file" ] && echo "session_file $(cat "$session_file")"
	done
	[ -f "$state_dir/done" ] && echo "done $(date -r "$state_dir/done" +%s)"
	[ -f "$state_dir/error" ] && echo "error $(tr '\n' ' ' <"$state_dir/error")"
	;;
escape_sequence)
	[ -f "$state_dir/escape_sequence" ] && echo "escape_sequence $(date -r "$state_dir/escape_sequence" +%s) $(cat "$state_dir/escape_sequence")"
	# The escape sequence stays on what it last said when the agent dies, so it counts only while the
	# agent runs.
	[ -n "$pid" ] && pgrep -P "$pid" "$3" >/dev/null && echo "alive"
	;;
esac
exit 0
