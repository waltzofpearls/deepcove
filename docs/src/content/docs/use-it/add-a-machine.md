---
title: Add another machine
description: Run sessions on a bigger box, and see every machine's sessions in one dashboard.
---

Each machine runs its own deepcove. Sign them all in to Tailscale with the same account, and they find each
other. Then one dashboard lists the sessions on all of them.

## Add one

1. [Install deepcove](/get-started/install/) on the new machine.
2. Sign it in to Tailscale. Run this on the new machine, and open the link it prints:

   ```sh
   deepcove network login
   ```

3. Sign in this machine too, the same way. Or click **Connect to Tailscale** on the **Settings** page.

Use the same Tailscale account on every machine. deepcove only lets that account in, so a machine signed in
as someone else never shows up.

## See your machines

The **Nodes** page lists every machine running deepcove, and the browsers signed in to your tailnet. Click
one to see its sessions and VMs.

Each machine shows up on your tailnet as `deepcove-` followed by its host name.

## Start a session on another machine

In **New session**, under **Where it runs**, pick the machine in **Node**. The menu shows up once you have
more than one.

A session lives on the machine that created it. Its files, VM and agent stay there.

## Open the dashboard from anywhere

Open [app.deepcove.dev](https://app.deepcove.dev) in any browser and click **Sign in with Tailscale**. It
finds your machines on its own. Nothing to install there.
