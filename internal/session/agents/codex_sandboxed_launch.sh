export PATH="$HOME/.local/bin:$PATH"
# deepcove starts this in the session's own folder. DEEPCOVE_WORKDIR is the machine's, which on a workspace
# VM is the folder every session's folder sits in, so the folder this starts in is the one to go back to.
session_folder=$PWD
# run_codex comes from codex_conversation.sh, which deepcove puts ahead of this script.

# Consecutive out-of-memory kills. An OOM is deterministic: the session's memory does not grow between
# attempts, so retrying forever just burns CPU and scrolls the explanation out of view. Reset on any
# other exit, so an unlucky one-off spike still gets retried.
ooms=0
while true; do
  cd "$session_folder" || exit 1
  run_codex codex
  ec=$?
  echo ""
  # 137 is SIGKILL, and in a VM with no swap that is almost always the kernel reclaiming memory. Say so
  # instead of printing a bare number: "code 137" sends you hunting through deepcove, when the answer is
  # that the session needs more memory. Checking dmesg keeps it honest, since SIGKILL has other sources.
  # Any OOM kill on record is enough: this VM runs one agent, so if the kernel reclaimed memory and our
  # process just took a SIGKILL, that is the same event. Matching on the process name looked tighter but
  # is fragile, because the agent re-execs its own bundled runtime under a different name.
  if [ "$ec" -eq 137 ] && dmesg 2>/dev/null | grep -qi "out of memory: killed process"; then
    ooms=$((ooms + 1))
    echo "codex ran out of memory and the kernel killed it."
    echo "This session has $(awk '/MemTotal/{printf "%d", $2/1024}' /proc/meminfo 2>/dev/null)MiB of RAM."
    dmesg 2>/dev/null | grep -i "out of memory: killed process" | tail -1
    if [ "$ooms" -ge 3 ]; then
      echo ""
      echo "Stopping after $ooms out-of-memory kills. Restarting cannot help: this session's memory is"
      echo "fixed at creation, so it would hit the same limit again."
      echo "Create a new session with more memory. Dropping to a shell so you can look around."
      # exec, not exit: leaving the window on a shell keeps the messages above readable and the pane
      # usable. Exiting would close the window and take the explanation with it.
      exec /bin/sh
    fi
    echo "Retrying in 3s in case it was a one-off spike ($ooms of 3)."
  else
    ooms=0
    echo "codex exited (code $ec); restarting in 3s. Switch to the Shell tab for a prompt"
  fi
  sleep 3
done
