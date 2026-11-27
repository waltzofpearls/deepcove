---
title: Connect to a session
description: Open a session in the browser, or in your own terminal with deepcove connect.
---

A session keeps running when you leave it. Come back to it from the browser or from a terminal, as often
as you like.

## In the browser

Run `deepcove open`, then click the session in the dashboard. It opens on the agent's terminal.

The buttons at the top stop, start or destroy the session, and open its settings. See
[Agent and Shell tabs](/sessions/tabs/) for the rest of the page.

## In a terminal

```sh
deepcove ls
deepcove connect <name>
```

`deepcove ls` lists the running sessions, and `deepcove ls -a` lists the stopped ones too. Connect by name,
by id, or by the first 4 or more characters of the id. If more than one session matches, it asks which.

- Press `Ctrl+\` to leave. The session keeps running.
- It only connects to a running session. To start a stopped one, open it in the dashboard and click the
  start button.
- Add `--shell` to start on the session's shell instead of the agent.
- It reaches sessions on [your other machines](/use-it/add-a-machine/) too, as long as deepcove runs on
  this one.

## More than one at a time

You can have the same session open in several places at once, such as a browser tab and a terminal. Each
one picks its own tab.
