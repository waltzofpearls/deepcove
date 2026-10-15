if ! cd "$DEEPCOVE_WORKDIR"; then
	echo "The folder this session works in is gone:"
	echo "  $DEEPCOVE_WORKDIR"
	echo "Recreate it, or make a new session. Dropping to a shell so you can look around."
	exec "${SHELL:-/bin/sh}"
fi
# run_codex comes from codex_conversation.sh, which deepcove puts ahead of this script. No out-of-memory
# branch here, because the machine has swap and this session has no memory cap.
while true; do
	run_codex "$DEEPCOVE_AGENT"
	ec=$?
	echo ""
	echo "codex exited ($ec), restarting in 3s"
	sleep 3
done
