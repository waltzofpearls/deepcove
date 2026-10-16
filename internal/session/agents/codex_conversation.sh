# Codex picks its own id for each conversation, so the first one a session starts is noted under the
# deepcove session id, and every later start resumes it. Codex names each conversation file after its id,
# and the file's first line records the folder Codex ran in and when the conversation began.
codex_sessions="$HOME/.codex/sessions"
deepcove_codex="$HOME/.codex/deepcove-conversations"
noted="$deepcove_codex/$DEEPCOVE_SESSION_ID"
# launches holds one file per session that has started Codex but not noted a conversation yet: when, and
# where. It stays after Codex exits, so a conversation that session began is never taken by another.
launches="$deepcove_codex/launches"

# conversation prints the arguments that continue this session's conversation, nothing before it has one.
conversation() {
  [ -s "$noted" ] || return
  id=$(cat "$noted")
  # A conversation whose file is gone cannot be resumed, so a new one is started and noted instead.
  if find "$codex_sessions" -name "rollout-*-$id.jsonl*" 2>/dev/null | grep -q .; then
    echo "resume $id"
  else
    rm -f "$noted"
  fi
}

# earlier_or_same is true when time $1 is no later than time $2. Both are UTC in ISO form, which sorts as
# text; the x keeps expr from reading them as numbers.
earlier_or_same() {
  [ "$(expr "x$1" \<= "x$2")" = 1 ]
}

# conversation_owner prints the session a conversation that began at $2 in folder $1 belongs to: of the
# sessions waiting in that folder, the last one to start Codex before it began. Codex starts a conversation
# right after it starts, so that is the one. Two that started in the same second cannot be told apart, so
# neither gets it, and each starts a new conversation next time rather than resume the other's.
conversation_owner() {
  folder=$1
  began=$2
  owner=
  latest=
  tied=
  for launch in "$launches"/*; do
    [ -f "$launch" ] || continue
    IFS=' ' read -r started launch_folder <"$launch" || continue
    [ "$launch_folder" = "$folder" ] || continue
    earlier_or_same "$started" "$began" || continue
    if [ "$started" = "$latest" ]; then
      tied=1
    elif [ -z "$latest" ] || earlier_or_same "$latest" "$started"; then
      owner=$(basename "$launch")
      latest=$started
      tied=
    fi
  done
  [ -z "$tied" ] && echo "$owner"
}

# note_conversation_once looks once for a new conversation file that belongs to this session, by
# conversation_owner, and notes its id. Files listed in $1 were there before this session started Codex.
# It fails when there is nothing to note yet.
note_conversation_once() {
  known=$1
  listed=$(mktemp)
  find "$codex_sessions" -name 'rollout-*.jsonl' >"$listed" 2>/dev/null
  found=
  while IFS= read -r file; do
    grep -qxF "$file" "$known" && continue
    name=$(basename "$file" .jsonl)
    id=${name#rollout-????-??-??T??-??-??-}
    first=$(head -n 1 "$file")
    folder=$(printf '%s\n' "$first" | sed -n 's/.*"cwd":"\([^"]*\)".*/\1/p')
    began=$(printf '%s\n' "$first" | sed -n "s/.*\"id\":\"$id\",\"timestamp\":\"\([^\"]*\)\".*/\1/p")
    [ -n "$folder" ] && [ -n "$began" ] || continue
    # Seconds are what a launch records, so that is what the comparison uses.
    [ "$(conversation_owner "$folder" "$(echo "$began" | cut -c1-19)")" = "$DEEPCOVE_SESSION_ID" ] || continue
    # Checked after the owner, since a session notes its conversation before it drops its launch: a
    # launch already gone means its note is already there to see.
    if cat "$deepcove_codex"/* 2>/dev/null | grep -qxF "$id"; then
      continue
    fi
    echo "$id" >"$noted"
    rm -f "$launches/$DEEPCOVE_SESSION_ID"
    found=1
    break
  done <"$listed"
  rm -f "$listed"
  [ -n "$found" ]
}

# note_conversation keeps looking until it has noted the conversation. Codex writes the file at the first
# prompt, so this runs for as long as Codex does.
note_conversation() {
  until note_conversation_once "$1"; do
    sleep 2
  done
}

# run_codex starts Codex, the binary at $1, on this session's conversation, and notes a new one when there
# is none yet. It returns Codex's exit code, which the restart loop reads.
#
# When deepcove stands between Codex and OpenAI, for the API key or the LLM proxy, it sets
# DEEPCOVE_LLM_BASE_URL and DEEPCOVE_LLM_TOKEN. Codex has no variable for its address, so they become a
# model provider of its own. The token names the session, and the host replaces it on the way out.
run_codex() {
  codex_binary=$1
  if [ -n "${DEEPCOVE_LLM_BASE_URL:-}" ]; then
    set -- -c model_provider=deepcove -c model_providers.deepcove.name=deepcove \
      -c "model_providers.deepcove.base_url=$DEEPCOVE_LLM_BASE_URL/v1" \
      -c model_providers.deepcove.env_key=DEEPCOVE_LLM_TOKEN
  else
    set --
  fi
  resume=$(conversation)
  if [ -n "$resume" ]; then
    # Unquoted on purpose: conversation prints a word and an id, which have to arrive as two arguments.
    # shellcheck disable=SC2086
    "$codex_binary" "$@" $resume
    return
  fi
  mkdir -p "$launches"
  known=$(mktemp)
  find "$codex_sessions" -name 'rollout-*.jsonl' >"$known" 2>/dev/null
  # Codex records its folder with any links resolved, so the launch does too.
  echo "$(date -u +%Y-%m-%dT%H:%M:%S) $(pwd -P)" >"$launches/$DEEPCOVE_SESSION_ID"
  note_conversation "$known" &
  noting=$!
  "$codex_binary" "$@"
  status=$?
  kill "$noting" 2>/dev/null
  wait "$noting" 2>/dev/null
  # One last look: a conversation Codex wrote after the watcher's last pass would otherwise be missed,
  # and from the next start on it counts as one that was already there.
  [ -s "$noted" ] || note_conversation_once "$known" || true
  rm -f "$known"
  return "$status"
}
