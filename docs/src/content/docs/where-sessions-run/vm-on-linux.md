---
title: VM on Linux
description: Sessions in their own Linux VM on Linux, with Cloud Hypervisor and KVM.
---

On Linux, deepcove runs each VM with Cloud Hypervisor, which needs KVM. Everything else works as on a Mac:
[sizes](/where-sessions-run/vm-on-macos/#pick-a-size), [shared folders](/where-sessions-run/vm-on-macos/#share-folders),
[one VM for many sessions](/where-sessions-run/vm-on-macos/#one-vm-for-many-sessions) and the
[network](/where-sessions-run/vm-on-macos/#network).

## KVM

deepcove needs to read and write `/dev/kvm`. Most machines have it, but cloud servers often don't, unless
they offer nested virtualization.

If `/dev/kvm` exists but you can't use it, add yourself to the kvm group, then reboot:

```sh
sudo usermod -aG kvm $USER
```

Until then, **New session** says KVM is missing, and you can still run sessions
[with no isolation](/where-sessions-run/no-sandbox/).

## Set it up

The first VM needs a one-time download: the Linux kernel, plus `cloud-hypervisor`, `virtiofsd` and `passt`,
the programs that run the VM, share its folders and give it a network. Click **Set up** where you see it, in
**New session** or on the machine's page under **Nodes**.

## Ubuntu 24.04 and AppArmor

Ubuntu 24.04 stops `passt` and `virtiofsd` from locking themselves down, unless an AppArmor profile lets
them. When deepcove runs as root, it installs the profile itself. Otherwise it saves the profile in its
data folder and logs the command to load it, such as:

```sh
sudo cp ~/.config/deepcove/deepcove.apparmor /etc/apparmor.d/deepcove
sudo apparmor_parser -r /etc/apparmor.d/deepcove
```

`deepcove service status` says where the log is.
