# Prebuilt deepcove session image for Claude Code.
#
# Bakes in everything the runtime setup script would otherwise install on first boot
# (~30s on a cold volume), so sessions started from this image boot in seconds and the
# user sees the Claude TUI almost immediately.
#
# Claude Code's native installer self-updates after first run, so the version baked in
# here is a floor, not a pin. Re-build periodically to keep first-boot version current.

FROM alpine:3.20

RUN apk add --no-cache curl bash tmux libgcc libstdc++ ripgrep

RUN curl -fsSL https://claude.ai/install.sh | bash \
    && ln -sf /root/.local/bin/claude /usr/local/bin/claude

# C.UTF-8 keeps tmux + less/vim/git-pager from substituting non-ASCII bytes with `_`.
# USE_BUILTIN_RIPGREP=0 forces Claude to use the system rg (musl) instead of its
# bundled glibc-linked binary which segfaults on Alpine.
ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    USE_BUILTIN_RIPGREP=0

# Entrypoint is supplied by deepcove at container create time (see buildEntrypoint in
# internal/session/service.go) — we leave CMD/ENTRYPOINT unset on purpose.
