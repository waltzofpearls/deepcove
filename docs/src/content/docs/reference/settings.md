---
title: Settings
description: The Settings page, and the settings each machine keeps.
---

## The Settings page

Click **Settings** in the dashboard.

| Section                        | What you set                                                              |
| ------------------------------ | ------------------------------------------------------------------------- |
| Appearance                     | Light, dark, or the same as your system                                   |
| Dashboard                      | Cards or a list, and how often it refreshes                               |
| Access from your other devices | Signs this machine in to Tailscale, so your other devices can reach it    |
| About                          | The command to update deepcove                                            |
| Claude Code login, Codex login | Per machine: who it's logged in as, what new sessions use, and an API key |
| LLM proxy                      | Per machine: a URL and headers to send the agents' requests through       |

## Settings each machine keeps

A few settings live in a file on each machine. Set them with `deepcove config`, then restart deepcove:

```sh
deepcove config set LOG_LEVEL=debug
deepcove service restart
```

| Setting                    | What it does                                                             |
| -------------------------- | ------------------------------------------------------------------------ |
| `ADDR`                     | Where the dashboard listens. `127.0.0.1:9000` by default                 |
| `LOG_LEVEL`                | How much goes in the log: `debug`, `info`, `warn` or `error`             |
| `DEEPCOVE_DEFAULT_RUNTIME` | Where new sessions run by default: `applevf`, `cloudhv` or `sandboxless` |

The file is `env` in deepcove's data folder: `~/Library/Application Support/deepcove` on macOS, and
`~/.config/deepcove` on Linux. Only you can read it. A variable you export in your shell wins over the
file.
