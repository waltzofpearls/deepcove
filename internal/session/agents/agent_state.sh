# deepcove reads what the agent is doing from this folder, with agent_state_read.sh. It lives under the
# temp folder, so a machine that restarts starts it empty.
state_dir="${TMPDIR:-/tmp}/deepcove-$DEEPCOVE_SESSION_ID"
mkdir -p "$state_dir"
# This script is the pane's own process, so the agent is one of its children while it runs.
echo $$ >"$state_dir/pid"
