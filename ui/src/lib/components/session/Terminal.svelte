<script lang="ts">
  // Full interactive xterm.js terminal attached to the session's
  // PTY WebSocket. Used in the /sessions/[id] route (full view).
  //
  // Wire protocol matches the server's deepcove.pty.v1:
  //   - binary frames in both directions for terminal I/O
  //   - text frames carry JSON control messages (resize today)
  //
  // Reconnect behaviour: if the WebSocket closes within a few seconds
  // of opening, we assume the container is still starting up (typical
  // after a stop → start cycle where the entrypoint Cmd re-runs apk
  // + tmux setup) and silently retry a handful of times with a short
  // backoff. After the budget is exhausted, surface "disconnected"
  // so the user can decide.

  import '@xterm/xterm/css/xterm.css';
  import type { Terminal as XTerm } from '@xterm/xterm';
  import { api } from '$lib/api/client';
  import type { BootstrapStatus } from '$lib/api/types';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

  // tmuxWindow optionally targets a specific tmux window in the
  // session. Omit/undefined → follow the session's current window
  // (legacy behavior). Used by the upcoming Agent/SSH mode toggle
  // and Split mode to pin each attached terminal to its own
  // window without affecting other clients.
  let { sessionId, tmuxWindow }: { sessionId: string; tmuxWindow?: string } = $props();

  let container: HTMLDivElement;
  let status = $state<'connecting' | 'open' | 'closed' | 'error'>('connecting');
  // While bootstrap is "installing" we don't open the WS — opening before tmux is up
  // errors in a tight retry loop.
  let bootstrap = $state<BootstrapStatus>('installing');
  let bootstrapLog = $state('');
  // Auto-scroll the log <pre> to bottom on every update.
  let logPreEl = $state<HTMLPreElement | undefined>(undefined);
  $effect(() => {
    if (!logPreEl || !bootstrapLog) return;
    logPreEl.scrollTop = logPreEl.scrollHeight;
  });

  const maxReconnectAttempts = 8;
  const earlyCloseThresholdMs = 3000;
  const bootstrapPollMs = 1000;

  $effect(() => {
    // Synchronous reads of reactive props so Svelte tracks them as
    // dependencies of this effect. The actual WS URL is built from
    // them inside connect(), which runs async after the xterm
    // dynamic import — async reads aren't tracked, so without these
    // lines the effect would never re-run on session/window switch
    // and the terminal would keep streaming the previous attach
    // while the toolbar (which uses its own reactive state) showed
    // the new target. Capturing into locals gives the closure
    // stable values for the lifetime of this effect run.
    const id = sessionId;
    const win = tmuxWindow;

    let term: XTerm | undefined;
    let fit: any;
    let ws: WebSocket | undefined;
    let ro: ResizeObserver | undefined;
    let resizeTimer: ReturnType<typeof setTimeout> | undefined;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let bootstrapTimer: ReturnType<typeof setTimeout> | undefined;
    let lastCols = 0;
    let lastRows = 0;
    let disposed = false;
    let attempts = 0;

    async function pollBootstrap(): Promise<BootstrapStatus> {
      while (!disposed) {
        try {
          const resp = await api.sessionBootstrap(id);
          if (disposed) return 'errored';
          bootstrap = resp.status;
          bootstrapLog = resp.log;
          if (resp.status !== 'installing') return resp.status;
        } catch {
          // Transient errors recover on the next poll.
        }
        await new Promise<void>((resolve) => {
          bootstrapTimer = setTimeout(resolve, bootstrapPollMs);
        });
      }
      return 'errored';
    }

    const sendResize = () => {
      if (!term || ws?.readyState !== 1) return;
      const { cols, rows } = term;
      if (cols === lastCols && rows === lastRows) return;
      lastCols = cols;
      lastRows = rows;
      ws.send(JSON.stringify({ type: 'resize', cols, rows }));
    };

    function connect() {
      if (disposed) return;
      status = 'connecting';
      ws = new WebSocket(api.attachWebSocketURL(id, win), 'deepcove.pty.v1');
      ws.binaryType = 'arraybuffer';

      const openedAt = Date.now();
      let everOpened = false;

      ws.onopen = () => {
        // If this effect run was already cleaned up (e.g. user
        // switched to another session before the WS finished its
        // handshake), don't touch the shared status — the new run
        // owns it now. Without this guard, an in-flight old WS
        // settling AFTER the new one opens can clobber status.
        if (disposed) return;
        everOpened = true;
        attempts = 0;
        status = 'open';
        sendResize();
        term?.focus();
      };
      ws.onerror = () => {
        // Browsers fire onerror right before onclose; let onclose
        // decide whether to retry.
      };
      ws.onclose = () => {
        // Same disposed-guard as onopen: if a new effect run has
        // already taken over (session switch), the old WS closing
        // must NOT write to the shared status. Previously this fell
        // through to `status = 'closed'`, which intermittently
        // clobbered the new run's `'open'` and showed a spurious
        // "disconnected" overlay over a working terminal.
        if (disposed) return;
        const lifetime = Date.now() - openedAt;
        // Retry if the connection died young — usually the server
        // failed to exec `tmux attach` because the container is
        // mid-startup.
        if (
          attempts < maxReconnectAttempts &&
          (!everOpened || lifetime < earlyCloseThresholdMs)
        ) {
          attempts++;
          status = 'connecting';
          // Backoff: 500ms, 700ms, 900ms, ... up to ~2s.
          const delay = Math.min(500 + attempts * 200, 2000);
          reconnectTimer = setTimeout(connect, delay);
          return;
        }
        status = 'closed';
      };
      ws.onmessage = (e) => {
        if (e.data instanceof ArrayBuffer) term?.write(new Uint8Array(e.data));
      };
    }

    (async () => {
      const final = await pollBootstrap();
      if (disposed || final === 'errored') return;

      // xterm.js measures cell width at init using whatever font is currently
      // resolvable; waiting for the webfont avoids locking in Menlo's metrics.
      try {
        await document.fonts.load('13px "JetBrains Mono"');
      } catch {
        /* not supported in this browser; fall through */
      }
      if (disposed) return;

      const [{ Terminal }, { FitAddon }, { WebLinksAddon }, { Unicode11Addon }] =
        await Promise.all([
          import('@xterm/xterm'),
          import('@xterm/addon-fit'),
          import('@xterm/addon-web-links'),
          import('@xterm/addon-unicode11'),
        ]);
      if (disposed) return;

      term = new Terminal({
        fontFamily: '"JetBrains Mono", Menlo, "Cascadia Code", Consolas, monospace',
        fontSize: 13,
        cursorBlink: true,
        cursorStyle: 'block',
        scrollback: 10_000,
        allowProposedApi: true,
        theme: readTheme(),
      });
      fit = new FitAddon();
      term.loadAddon(fit);
      term.loadAddon(new WebLinksAddon());
      term.loadAddon(new Unicode11Addon());
      term.unicode.activeVersion = '11';
      // No @xterm/addon-webgl on purpose — its sprite atlas can't draw many glyphs
      // Claude's TUI uses and falls back to underline placeholders. DOM renderer
      // delegates to browser text rendering, which handles everything correctly.
      term.open(container);
      fit.fit();

      const enc = new TextEncoder();
      term.onData((s) => {
        if (ws?.readyState === 1) ws.send(enc.encode(s));
      });

      ro = new ResizeObserver(() => {
        clearTimeout(resizeTimer);
        resizeTimer = setTimeout(() => {
          try {
            fit.fit();
            sendResize();
          } catch {
            /* xterm not yet mounted */
          }
        }, 150);
      });
      ro.observe(container);

      connect();
    })();

    return () => {
      disposed = true;
      clearTimeout(resizeTimer);
      clearTimeout(reconnectTimer);
      clearTimeout(bootstrapTimer);
      ro?.disconnect();
      ws?.close();
      term?.dispose();
    };
  });

  function readTheme() {
    const s = getComputedStyle(document.documentElement);
    const v = (n: string, fb: string) => s.getPropertyValue(n).trim() || fb;
    return {
      background: v('--term-bg', '#0b0d10'),
      foreground: v('--term-fg', '#e6e6e6'),
      cursor: v('--term-cursor', '#e6e6e6'),
      selectionBackground: v('--term-sel', '#264f78'),
    };
  }
</script>

<div class="relative h-full w-full bg-[var(--term-bg)]">
  <div bind:this={container} class="h-full w-full"></div>

  {#if bootstrap === 'installing'}
    <div
      class="absolute inset-0 flex flex-col gap-4 bg-[var(--term-bg)] p-6 text-foreground"
    >
      <div class="flex shrink-0 flex-col items-center gap-2">
        <div class="flex items-center gap-2 text-sm">
          <Loader2 class="size-4 animate-spin text-primary" />
          Setting up your session…
        </div>
        <p class="max-w-md text-center text-xs text-muted-foreground">
          Installing the coding agent and its dependencies. This is one-time per agent;
          future sessions of the same agent skip this step.
        </p>
      </div>
      {#if bootstrapLog}
        <pre
          bind:this={logPreEl}
          class="scrollbar-hide min-h-0 w-full flex-1 overflow-y-auto rounded-md border border-border bg-card/50 px-3 py-2 font-mono text-[11px] leading-tight text-muted-foreground"
        >{bootstrapLog}</pre>
      {/if}
    </div>
  {:else if bootstrap === 'errored'}
    <div
      class="absolute inset-0 flex flex-col gap-4 bg-[var(--term-bg)] p-6 text-foreground"
    >
      <div class="flex shrink-0 flex-col items-center gap-2">
        <div class="flex items-center gap-2 text-sm text-destructive">
          <TriangleAlert class="size-4" />
          Setup failed
        </div>
        <p class="max-w-md text-center text-xs text-muted-foreground">
          The container's entrypoint setup script exited non-zero. Use SSH mode to
          inspect, or destroy and recreate the session.
        </p>
      </div>
      {#if bootstrapLog}
        <pre
          bind:this={logPreEl}
          class="scrollbar-hide min-h-0 w-full flex-1 overflow-y-auto rounded-md border border-destructive/50 bg-destructive/5 px-3 py-2 font-mono text-[11px] leading-tight"
        >{bootstrapLog}</pre>
      {/if}
    </div>
  {:else if status === 'connecting'}
    <div
      class="pointer-events-none absolute inset-0 flex items-center justify-center bg-[var(--term-bg)]/80 text-sm text-muted-foreground"
    >
      connecting…
    </div>
  {:else if status === 'closed' || status === 'error'}
    <div
      class="pointer-events-none absolute inset-0 flex items-center justify-center bg-[var(--term-bg)]/80 text-sm text-muted-foreground"
    >
      {status === 'error' ? 'connection error' : 'disconnected'}
    </div>
  {/if}
</div>

<style>
  :global(.xterm-viewport) {
    background: transparent !important;
  }
</style>
