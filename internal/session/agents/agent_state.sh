# deepcove reads what the agent is doing from this folder, with agent_state_read.sh. With no sandbox it
# is a private folder deepcove names. In a VM, root is the only user, so /tmp is fine.
state_dir="${DEEPCOVE_AGENT_STATE_DIR:-/tmp/deepcove-$DEEPCOVE_SESSION_ID}"
# Exported for the agent's hooks, which name the folder through it rather than by a quoted path.
export DEEPCOVE_AGENT_STATE_DIR="$state_dir"
mkdir -p "$state_dir"
# This script is the pane's own process, so the agent is one of its children while it runs.
echo $$ >"$state_dir/pid"
