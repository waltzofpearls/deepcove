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

  let { sessionId }: { sessionId: string } = $props();

  let container: HTMLDivElement;
  let status = $state<'connecting' | 'open' | 'closed' | 'error'>('connecting');

  const MAX_RECONNECT_ATTEMPTS = 8;
  const EARLY_CLOSE_THRESHOLD_MS = 3000;

  $effect(() => {
    let term: XTerm | undefined;
    let fit: any;
    let ws: WebSocket | undefined;
    let ro: ResizeObserver | undefined;
    let resizeTimer: ReturnType<typeof setTimeout> | undefined;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let lastCols = 0;
    let lastRows = 0;
    let disposed = false;
    let attempts = 0;

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
      ws = new WebSocket(api.attachWebSocketURL(sessionId), 'deepcove.pty.v1');
      ws.binaryType = 'arraybuffer';

      const openedAt = Date.now();
      let everOpened = false;

      ws.onopen = () => {
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
        const lifetime = Date.now() - openedAt;
        // Retry if the connection died young — usually the server
        // failed to exec `tmux attach` because the container is
        // mid-startup.
        if (
          !disposed &&
          attempts < MAX_RECONNECT_ATTEMPTS &&
          (!everOpened || lifetime < EARLY_CLOSE_THRESHOLD_MS)
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
      const [{ Terminal }, { FitAddon }, { WebLinksAddon }, { WebglAddon }] =
        await Promise.all([
          import('@xterm/xterm'),
          import('@xterm/addon-fit'),
          import('@xterm/addon-web-links'),
          import('@xterm/addon-webgl'),
        ]);
      if (disposed) return;

      term = new Terminal({
        fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, monospace',
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
      term.open(container);
      try {
        const webgl = new WebglAddon();
        webgl.onContextLoss(() => webgl.dispose());
        term.loadAddon(webgl);
      } catch {
        /* fall back to DOM renderer */
      }
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
  {#if status === 'connecting'}
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
