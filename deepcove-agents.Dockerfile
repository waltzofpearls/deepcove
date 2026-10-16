# Prebuilt deepcove session image with every agent installed: Claude Code and Codex.
#
# Bakes in everything the agents' setup scripts would otherwise install on first boot
# (~30s on a cold volume), so sessions started from this image boot in seconds.
#
# Both agents update themselves after first run, so the versions baked in here are a
# floor, not a pin. The weekly rebuild keeps first boot current.

FROM alpine:3.20

# procps-ng: Codex's background server needs the full ps, which busybox lacks.
# bubblewrap: the sandbox Codex runs its own commands in.
RUN apk add --no-cache curl bash git tmux libgcc libstdc++ ripgrep procps-ng bubblewrap

# Each installer is downloaded before it runs, and the build checks the agent is there. Piped, a failed
# download hands the shell nothing to run, which it counts as success, and the image ships without it.
RUN curl -fsSL https://claude.ai/install.sh -o /tmp/claude-install.sh \
    && bash /tmp/claude-install.sh \
    && test -x /root/.local/bin/claude \
    && ln -sf /root/.local/bin/claude /usr/local/bin/claude \
    && rm /tmp/claude-install.sh

RUN curl -fsSL https://chatgpt.com/codex/install.sh -o /tmp/codex-install.sh \
    && sh /tmp/codex-install.sh \
    && test -x /root/.local/bin/codex \
    && ln -sf /root/.local/bin/codex /usr/local/bin/codex \
    && rm /tmp/codex-install.sh

# C.UTF-8 keeps tmux + less/vim/git-pager from substituting non-ASCII bytes with `_`.
# USE_BUILTIN_RIPGREP=0 forces Claude to use the system rg (musl) instead of its
# bundled glibc-linked binary which segfaults on Alpine.
ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    USE_BUILTIN_RIPGREP=0

# Put the native installers' bin dir on PATH. The /usr/local/bin symlinks above make
# both agents launchable, but Claude Code separately checks that ~/.local/bin is on
# PATH (for its self-update) and warns when it isn't.
ENV PATH="/root/.local/bin:${PATH}"

# Entrypoint is supplied by deepcove at container create time (see buildEntrypoint in
# internal/session/service.go) — we leave CMD/ENTRYPOINT unset on purpose.
