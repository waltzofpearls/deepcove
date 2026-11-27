---
title: Your first session
description: Create a session, pick where it runs, and log the agent in.
---

1. Run `deepcove open` and click **New session**.
2. At the top, the dialog says where the session runs, such as **Safest, in its own VM**. It starts on your
   last VM choice on this machine, or else on its own VM wherever VMs can run. Click **Change** to pick
   another place or another size. The first VM on a machine needs a one-time download, so click **Set up**
   if you see it.
3. Pick an **Agent**: Claude Code or Codex.
4. Pick a **Work folder**. The agent works there with your real files.
5. Click **Create session**. The first time, it says **Download & create**, since the agent's image is
   downloaded first.

The session opens with the agent running in its terminal.

## Where it runs

| In the dialog                              | What it is                                                                        |
| ------------------------------------------ | --------------------------------------------------------------------------------- |
| Safest, in its own VM                      | A Linux VM for this session alone. The agent can only reach the folders you share |
| Shared, in one VM with your other sessions | One VM your sessions share, each in its own folder. Starts in seconds             |
| No isolation, right on the node            | Runs on the machine itself. Fastest, but the agent sees everything                |

Use a VM unless the machine is already the boundary, such as a cloud box set aside for agents. More on each:
[VM on macOS](/where-sessions-run/vm-on-macos/), [VM on Linux](/where-sessions-run/vm-on-linux/) and
[No sandbox](/where-sessions-run/no-sandbox/).

## The work folder

It depends on where the session runs:

| Where it runs | The work folder                                                                                                                                                                              |
| ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Its own VM    | Starts as your home folder. Pick a project folder inside it, since your whole home folder can't be shared. Clear the field for an empty folder in the VM, which is lost when the VM restarts |
| Shared VM     | Must be inside the folder the shared VM can see                                                                                                                                              |
| No isolation  | deepcove makes a folder for the session, which is deleted with it. Switch on **Use my own folder** to work in yours instead                                                                  |

## Log the agent in

When there is more than one way, the dialog asks:

| Where it runs | The choices                                                                |
| ------------- | -------------------------------------------------------------------------- |
| Its own VM    | My shared login, Its own login, My API key, and LLM proxy if one is set up |
| Shared VM     | The workspace VM's login, My API key, and LLM proxy if one is set up       |
| No isolation  | The login on this machine, and LLM proxy if one is set up                  |

To log in inside a session, run `/login` in Claude Code. In Codex, sign in with a device code, since a
browser login cannot reach back into a VM.

## Pick a size

A session's own VM is **Small** (1 CPU, 1 GB), **Medium** (1 CPU, 2 GB) or **Custom**. Medium is the
default. The dialog says how many fit on your machine.

## Leave and come back

Close the browser whenever you like: the session keeps running.
[Connect to it again](/use-it/connect/) from the browser or a terminal.
