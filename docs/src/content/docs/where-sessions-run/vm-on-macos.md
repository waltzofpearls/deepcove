---
title: VM on macOS
description: Sessions in their own Linux VM on a Mac, with Apple Virtualization.
---

On a Mac, deepcove runs each VM with Apple's Virtualization framework, which is built into macOS. There is
nothing else to install.

## Set it up

The first VM needs a one-time download of the Linux kernel. Click **Set up** where you see it: in
**New session**, on the machine's page under **Nodes**, or in the setup steps. It comes from the same
release as your deepcove.

## Pick a size

A session's own VM is **Small** (1 CPU, 1 GB), **Medium** (1 CPU, 2 GB) or **Custom**. Medium is the
default. Change it later in the [session's settings](/sessions/tabs/#session-settings).

## Share folders

The work folder you pick is shared into the VM, so the agent works on your real files. Add more in the
session's settings. Anything you don't share stays out of reach.

## One VM for many sessions

**Shared, in one VM with your other sessions** runs sessions together in a workspace VM. Each one gets its
own folder, and they start in seconds, since the VM is already running.

Create it the first time you pick it in **New session**. You choose the **Folder this VM can see**, where it
shows up **In the VM**, and its size, a share of your machine's spare memory.

Or click **Create a workspace VM** on the machine's page under **Nodes**. There you can also add a
**Setup script**, which runs once when the VM is built, such as installing your tools.

Every session in the workspace VM can read and change everything in that folder.

## Network

A VM can reach the internet, but not your home or office network. To let it reach one address there, such
as a database, start deepcove with `--sandbox-can-reach`:

```sh
deepcove start --sandbox-can-reach 192.168.1.20
```

Under the background service, add it after `--`:

```sh
deepcove service install -- --sandbox-can-reach 192.168.1.0/24
deepcove service restart
```
