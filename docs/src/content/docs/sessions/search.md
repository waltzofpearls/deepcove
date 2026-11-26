---
title: Search terminal history
description: Find text an agent printed, even after it scrolled off the screen.
---

Find text the agent printed, even after it scrolled off the screen. A session keeps the last 10,000 lines of
each terminal.

## Open the search bar

- **With the terminal in focus:** press `Cmd+F` on a Mac, or `Ctrl+Shift+F` on Windows and Linux. Plain
  `Ctrl+F` stays with the shell.
- **On any device:** click the search button in the session's header, while the session is running.

In Split, the search button searches the agent's terminal.

## Move between matches

| Key           | What it does                             |
| ------------- | ---------------------------------------- |
| `Enter`       | Goes to the next older match             |
| `Shift+Enter` | Goes to the next newer match             |
| `Esc`         | Closes search, back to the live terminal |

On a phone, use the up and down arrows in the bar, and the X to close it.

## Claude Code

Claude Code runs full screen, which keeps no history, so search only covers what is on the screen. When the
text isn't there, the bar says **Not on screen**. To search the whole conversation, use Claude's own search:
press `Ctrl+O`, then `/`.

:::note
Two browsers on the same terminal share one search, so closing it in one stops it in the other too, even
though the other's bar stays open.
:::
