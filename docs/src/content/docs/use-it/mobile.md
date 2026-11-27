---
title: Use it on mobile
description: Check on your agents and answer them from a phone or tablet.
---

Your sessions keep running on your machines, so a phone or tablet only needs a browser to reach them.

## Open it

First, sign your machine in to Tailscale with `deepcove network login`, as in
[Add another machine](/use-it/add-a-machine/). Then pick one:

- **Any browser:** open [app.deepcove.dev](https://app.deepcove.dev) and click **Sign in with Tailscale**.
- **With the Tailscale app on the device:** open your machine's own address. `deepcove network status`
  prints it as `url`.

The machine's address uses HTTPS once MagicDNS and HTTPS certificates are on in your Tailscale admin.
Until then it is plain `http`.

## The key bar

Phones and tablets get a row of keys under the terminal, for the keys a touch keyboard lacks:

| Key            | What it does                                                    |
| -------------- | --------------------------------------------------------------- |
| esc            | Escape                                                          |
| Tab, Shift Tab | Tab forward or back                                             |
| ctrl           | Tap once for the next key only. Tap again to keep it on         |
| Arrows         | Move the cursor, or pick an option in the agent's menus         |
| Enter          | Return                                                          |
| Keyboard icon  | Shows or hides the touch keyboard. The key bar stays either way |

## Get around

- **Scroll:** swipe up or down on the terminal.
- **Switch sessions:** tap one in the row of sessions at the top.
- **Search:** tap the search button in the session's header. See
  [Search terminal history](/sessions/search/).

Split view needs a wider screen, so a phone shows the agent or the shell, one at a time.
