if ! cd "$DEEPCOVE_WORKDIR"; then
	echo "The folder this session works in is gone:"
	echo "  $DEEPCOVE_WORKDIR"
	echo "Recreate it, or make a new session. Dropping to a shell so you can look around."
	exec "${SHELL:-/bin/sh}"
fi
# Same conversation rule as the guest: no file for our session id means start it, a file means continue
# it. No out-of-memory branch here, because the machine has swap and this session has no memory cap.
while true; do
	if ls "$HOME"/.claude/projects/*/"$DEEPCOVE_SESSION_ID".jsonl >/dev/null 2>&1; then
		"$DEEPCOVE_AGENT" --resume "$DEEPCOVE_SESSION_ID"
	else
		"$DEEPCOVE_AGENT" --session-id "$DEEPCOVE_SESSION_ID"
	fi
	ec=$?
	echo ""
	echo "claude exited ($ec), restarting in 3s"
	sleep 3
done
