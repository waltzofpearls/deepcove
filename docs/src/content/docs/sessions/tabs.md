---
title: Agent and Shell tabs
description: The agent's terminal, a plain shell next to it, and the session's settings.
---

Every session has two terminals, and the switch at the top of the session picks which one you see.

| Tab   | What it is                                                          |
| ----- | ------------------------------------------------------------------- |
| Agent | The agent, such as Claude Code or Codex                             |
| Shell | A plain shell in the same VM, starting in the session's work folder |
| Split | Both side by side. Only on a wide screen                            |

Use the shell to look at what the agent did, run the tests yourself, or check `git status`, without
interrupting the agent.

From a terminal, `deepcove connect --shell <name>` starts on the shell.

## Keys

- `Shift+Enter` adds a new line without sending.
- To copy from the agent's terminal, select the text, then press `Cmd+C` on a Mac or `Ctrl+Shift+C`
  elsewhere.
- `Cmd+F` or `Ctrl+Shift+F` [searches the terminal](/sessions/search/).

## Session settings

Click the settings button at the top of the session. It shows where the session runs, its folder and its
size, and lets you change:

- **The login.** Pick another way to log the agent in, then save. When the agent has to restart for it,
  the button says **Save and restart agent**.
- **The VM's size.** It takes effect when the VM restarts, now or later.
- **Shared folders.** Click **Add a folder** to share another folder from your machine. Saving recreates
  the VM. The agent's home and your shared folders stay, but packages it installed are gone.
