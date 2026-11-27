---
title: What is deepcove
description: Run coding agents in their own VMs, across all your machines, from one dashboard.
---

deepcove runs coding agents such as Claude Code and Codex on your own machines, and puts each agent's
terminal in your browser.

- **A VM for every session.** A session can boot its own small Linux VM. The agent can install, delete and
  break things in there without touching the rest of your machine. It can still change the folders you
  share with it.
- **Many agents at once.** Each session has its own terminal and folder, and keeps running after you close
  the browser.
- **All your machines in one place.** Sign each machine in to Tailscale, and one dashboard lists every
  session on all of them.

Nothing is published to the internet. The dashboard listens on 127.0.0.1, and over your tailnet only your
own Tailscale identity is let in.

## Where sessions run

When you create a session, you pick one of three places:

| In the dialog                              | What it is                                                                        |
| ------------------------------------------ | --------------------------------------------------------------------------------- |
| Safest, in its own VM                      | A Linux VM for this session alone. The agent can only reach the folders you share |
| Shared, in one VM with your other sessions | One VM your sessions share, each in its own folder. Starts in seconds             |
| No isolation, right on the node            | Runs on the machine itself. Fastest, but the agent sees everything                |

A VM runs on Apple Virtualization on macOS, and on Cloud Hypervisor on Linux, which needs KVM. Running on the
machine itself needs tmux. Install it, then restart deepcove.

Use a VM unless the machine is already the boundary, such as a cloud box set aside for agents.

## Next

[Install deepcove](/get-started/install/), then [start your first session](/get-started/first-session/).
