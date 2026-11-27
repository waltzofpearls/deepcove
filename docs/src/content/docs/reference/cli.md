---
title: CLI commands
description: Every deepcove command, and what it does.
---

## Sessions

| Command                   | What it does                                                             |
| ------------------------- | ------------------------------------------------------------------------ |
| `deepcove claude`         | Creates a Claude Code session. `deepcove codex` creates a Codex one      |
| `deepcove ls`             | Lists running sessions on all your machines. `-a` adds stopped ones      |
| `deepcove connect <name>` | [Connects](/use-it/connect/) to a session. `--shell` starts on the shell |

Flags for creating a session:

| Flag                 | What it does                                                                                           |
| -------------------- | ------------------------------------------------------------------------------------------------------ |
| `--name`             | Names the session. Without it, deepcove picks a name                                                   |
| `--runtime`          | `applevf`, `cloudhv` or `sandboxless`. Without it, [this machine's default](/reference/settings/)      |
| `--share host:guest` | Shares a folder into the VM. Add `:ro` for read only. Repeatable                                       |
| `--work-dir`         | The folder the agent works in, with `sandboxless` only. In a VM, use `--share ~/code/myapp:/workspace` |

```sh
deepcove claude --name auth-redesign --share ~/code/myapp:/workspace
```

The longer forms work too: `deepcove session claude`, `deepcove session connect`, `deepcove session list`.

## deepcove itself

| Command                    | What it does                                                       |
| -------------------------- | ------------------------------------------------------------------ |
| `deepcove open`            | Opens the dashboard. `--no-browser` prints a link to paste instead |
| `deepcove status`          | Says whether deepcove runs, and where to reach it                  |
| `deepcove start`           | Runs deepcove in the foreground, instead of in the background      |
| `deepcove service install` | Runs deepcove in the background, starting with your login          |
| `deepcove service status`  | Says whether the background service runs, and where its logs are   |
| `deepcove service restart` | Restarts it. `start`, `stop` and `uninstall` work too              |
| `deepcove upgrade`         | Replaces deepcove with the newest release. `--check` only looks    |
| `deepcove version`         | Prints the version                                                 |

## Tailscale

| Command                       | What it does                                                  |
| ----------------------------- | ------------------------------------------------------------- |
| `deepcove network login`      | Signs this machine in to Tailscale. It prints a link to open  |
| `deepcove network status`     | Shows this machine's name, tailnet and address                |
| `deepcove network disconnect` | Leaves for now. `network login` reconnects without signing in |
| `deepcove network logout`     | Signs out. This machine leaves your tailnet                   |

## Settings and repair

| Command                         | What it does                                                  |
| ------------------------------- | ------------------------------------------------------------- |
| `deepcove config list`          | Lists the [settings](/reference/settings/) this machine keeps |
| `deepcove config set KEY=VALUE` | Stores a setting. `config unset KEY` removes it               |
| `deepcove db check`             | Checks the local database. Stop deepcove first                |
| `deepcove db repair`            | Repairs the local database. Stop deepcove first               |
