---
title: Upload a file to a session
description: Paste or drop a file on the terminal, and the agent gets its path.
---

Paste or drop a file on the session's terminal. deepcove copies it into the session and types its path, so
you can tell the agent what to do with it, such as "look at this screenshot".

It works for any kind of file, such as a screenshot, a log or a PDF. Plain text you paste is still typed as
usual.

## From a terminal

In `deepcove connect`, paste or drop the file from your file manager. The terminal pastes its path, and
deepcove uploads the file and types the path inside the session instead. Several files at once work too.

## Limits

- Each file can be up to 25 MiB.
- In a VM, files land in `/tmp/.uploads`, inside the VM only. They're gone when the VM stops or restarts,
  so copy one into a folder shared from your machine to keep it.
- With no isolation, they land in deepcove's cache folder.
- Uploads are deleted with the session, except in a shared VM. There, they stay until the VM restarts, and
  the other sessions on it can read them.
