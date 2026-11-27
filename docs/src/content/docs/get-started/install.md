---
title: Install
description: Install deepcove on macOS or Linux with one command, and open the dashboard.
---

```sh
curl -fsSL https://deepcove.dev/install.sh | sh
```

It installs to `~/.local/bin` with no sudo and checks the download against the release's checksums. Then
it starts deepcove in the background, and deepcove starts again on its own from then on.

If `~/.local/bin` is not on your PATH, the installer offers to add it to your shell's startup file. Open a
new terminal afterwards, or do what the installer prints, so the `deepcove` command is found.

## What you need

- **macOS**, or **Linux** with glibc. Alpine uses musl, so it does not work there.
- **KVM on Linux**, for sessions in a VM. Without it, sessions can still run on the machine itself.

If Linux has `/dev/kvm` but you cannot use it, the installer tells you. Add yourself to the kvm group, then
reboot, since deepcove keeps running in the background with your old groups until then:

```sh
sudo usermod -aG kvm $USER
```

## Open the dashboard

On your own computer, the installer opens it for you. To open it again:

```sh
deepcove open
```

On a machine you reach over SSH, sign it in to Tailscale first:

```sh
deepcove network login
```

Then open [app.deepcove.dev](https://app.deepcove.dev) in any browser and sign in to Tailscale there too.

## Who can reach it

Nothing is published to the internet. The dashboard listens on 127.0.0.1, so only this machine reaches it.
Over your tailnet, only your own Tailscale account gets in.

## Check that it runs

```sh
deepcove service status
```

It also says where the logs are.

## Installer options

Set these before `sh`:

| Variable                | What it does                                         |
| ----------------------- | ---------------------------------------------------- |
| `DEEPCOVE_INSTALL_DIR`  | Installs to this folder instead of `~/.local/bin`    |
| `DEEPCOVE_VERSION`      | Installs this release, such as `v0.1.1`              |
| `DEEPCOVE_NO_SERVICE=1` | Installs without starting deepcove in the background |

```sh
curl -fsSL https://deepcove.dev/install.sh | DEEPCOVE_NO_SERVICE=1 sh
```

Without the background service, run `deepcove start` yourself.

## Upgrade

```sh
deepcove upgrade
```

It checks the download before replacing anything. If deepcove runs in the background, it restarts it, and
sessions in a VM restart too. If you started it with `deepcove start`, restart that yourself. A work folder
from your machine and the agent's home folder stay as they were. Anything else in the VM starts fresh, such
as packages the agent installed, and an empty work folder made inside the VM.
