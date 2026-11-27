---
title: No sandbox
description: Run a session right on the machine, with no VM around it.
---

**No isolation, right on the node** runs the agent on the machine itself, as your user. It's the fastest
way, but the agent sees everything you can: your files, your network and your logins.

Use it when the machine is already the boundary, such as a cloud box set aside for agents, or a machine
where VMs can't run.

## What it needs

tmux. If it's missing, **New session** says so. Install it, then restart deepcove:

```sh
deepcove service restart
```

## The work folder

By default, deepcove makes an empty folder for the session and deletes it with the session. To work in your
own folder, switch on **Use my own folder** and pick it. Deleting the session leaves your folder alone.

## Login

The agent uses **The login on this machine**, whatever this machine is already logged in as. If the agent
isn't installed, deepcove installs its own copy for these sessions. With an LLM proxy set up, you can pick
**LLM proxy** instead, as long as the proxy has no headers set. To send headers, use a VM.

## Make it the default

deepcove never picks it for you. Pick it in **New session**, or name it each time from a terminal:

```sh
deepcove claude --runtime sandboxless
```

Or make it this machine's default, for the dialog too, then restart deepcove:

```sh
deepcove config set DEEPCOVE_DEFAULT_RUNTIME=sandboxless
deepcove service restart
```
