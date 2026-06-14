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
  import type { DeepcoveSocket } from '$lib/api/transport';
  import type { BootstrapStatus } from '$lib/api/types';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import Check from '@lucide/svelte/icons/check';

  // tmuxWindow optionally targets a specific tmux window in the
  // session. Omit/undefined → follow the session's current window
  // (legacy behavior). Used by the upcoming Agent/SSH mode toggle
  // and Split mode to pin each attached terminal to its own
  // window without affecting other clients.
  let { sessionId, tmuxWindow }: { sessionId: string; tmuxWindow?: string } = $props();

  let container: HTMLDivElement;
  let status = $state<'connecting' | 'open' | 'closed' | 'error'>('connecting');
  // Upload indicator for terminal-focused file paste/drop. uploading is a
  // count (sequential, but a count keeps the {#if} simple); uploadPct is the
  // live XHR progress; uploadNote/uploadOk drive the brief done/error flash.
  let uploading = $state(0);
  let uploadLabel = $state('');
  let uploadPct = $state(0);
  let uploadNote = $state('');
  let uploadOk = $state(true);
  let uploadNoteTimer: ReturnType<typeof setTimeout> | undefined;
  function flashNote(msg: string, ok: boolean) {
    uploadNote = msg;
    uploadOk = ok;
    clearTimeout(uploadNoteTimer);
    uploadNoteTimer = setTimeout(() => (uploadNote = ''), ok ? 1500 : 3500);
  }
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
    let ws: DeepcoveSocket | undefined;
    let ro: ResizeObserver | undefined;
    let onPaste: ((e: ClipboardEvent) => void) | undefined;
    let onDragOver: ((e: DragEvent) => void) | undefined;
    let onDrop: ((e: DragEvent) => void) | undefined;
    let resizeTimer: ReturnType<typeof setTimeout> | undefined;
    let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
    let bootstrapTimer: ReturnType<typeof setTimeout> | undefined;
    let lastCols = 0;
    let lastRows = 0;
    let disposed = false;
    let attempts = 0;
    // Aborts any in-flight bootstrap fetch when this effect tears down. Each
    // leaked fetch holds a keep-alive connection slot until the server
    // responds, and only takes a few to push past Firefox's per-origin
    // connection cap — see client.ts for the full story.
    const bootstrapAC = new AbortController();

    async function pollBootstrap(): Promise<BootstrapStatus> {
      while (!disposed) {
        try {
          const resp = await api.sessionBootstrap(id, bootstrapAC.signal);
          if (disposed) return 'errored';
          bootstrap = resp.status;
          bootstrapLog = resp.log;
          if (resp.status !== 'installing') return resp.status;
        } catch {
          // Aborts and transient errors both land here.
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
      ws = api.openAttachSocket(id, win);
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
      // Swallow the mouse-tracking DECSET enables so Claude's TUI can't grab
      // the mouse — then a normal drag selects and Cmd-C copies natively.
      // (Bracketed paste, alt screen, etc. pass through.) DECSET ?_h codes:
      //   1000 = click   1001 = highlight   1002 = drag   1003 = any-motion
      const mouseModes = new Set([1000, 1001, 1002, 1003]);
      // Return true => handled (suppressed), only when every param is a mouse mode.
      term.parser.registerCsiHandler(
        { prefix: '?', final: 'h' },
        (params) =>
          params.length > 0 &&
          params.every((p) => mouseModes.has(typeof p === 'number' ? p : p[0])),
      );
      // No @xterm/addon-webgl on purpose — its sprite atlas can't draw many glyphs
      // Claude's TUI uses and falls back to underline placeholders. DOM renderer
      // delegates to browser text rendering, which handles everything correctly.
      term.open(container);
      fit.fit();

      const enc = new TextEncoder();
      term.onData((s) => {
        if (ws?.readyState === 1) ws.send(enc.encode(s));
      });

      // Terminal-focused paste/drop of files & images. Upload each into the
      // container and inject its in-container path into the prompt (the
      // native-Claude path-paste UX; the chip isn't reproducible in a Linux
      // container). Plain-text paste falls through to xterm untouched.
      async function uploadAndInject(files: File[]) {
        for (const file of files) {
          uploading += 1;
          uploadLabel = file.name || 'file';
          uploadPct = 0;
          try {
            const { path } = await api.uploadFile(id, file, (pct) => (uploadPct = pct));
            term?.paste(path + ' ');
            flashNote(`Uploaded ${file.name || 'file'}`, true);
          } catch (err) {
            flashNote(`Upload failed: ${err instanceof Error ? err.message : String(err)}`, false);
          } finally {
            uploading -= 1;
          }
        }
      }
      // Capture phase + stopPropagation so xterm's own (text-only) paste
      // handler doesn't also fire on a file paste.
      onPaste = (e: ClipboardEvent) => {
        const files = filesFrom(e.clipboardData);
        if (files.length === 0) return; // text paste → let xterm handle it
        e.preventDefault();
        e.stopPropagation();
        void uploadAndInject(files);
      };
      onDragOver = (e: DragEvent) => e.preventDefault();
      onDrop = (e: DragEvent) => {
        const files = filesFrom(e.dataTransfer);
        if (files.length === 0) return;
        e.preventDefault();
        void uploadAndInject(files);
      };
      container.addEventListener('paste', onPaste, { capture: true });
      container.addEventListener('dragover', onDragOver);
      container.addEventListener('drop', onDrop);

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
      bootstrapAC.abort();
      clearTimeout(resizeTimer);
      clearTimeout(reconnectTimer);
      clearTimeout(bootstrapTimer);
      if (onPaste) container.removeEventListener('paste', onPaste, { capture: true });
      if (onDragOver) container.removeEventListener('dragover', onDragOver);
      if (onDrop) container.removeEventListener('drop', onDrop);
      ro?.disconnect();
      ws?.close();
      term?.dispose();
    };
  });

  // filesFrom collects File objects from a paste/drop — covers both real
  // files and pasted image bytes (clipboard items of kind "file").
  function filesFrom(dt: DataTransfer | null): File[] {
    if (!dt) return [];
    const out: File[] = [];
    for (const item of dt.items ?? []) {
      if (item.kind === 'file') {
        const f = item.getAsFile();
        if (f) out.push(f);
      }
    }
    if (out.length === 0) {
      for (const f of dt.files ?? []) out.push(f);
    }
    return out;
  }

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

  {#if uploading > 0 || uploadNote}
    <!-- Bottom bar overlaying the tmux status line. Left-aligned so it sits
         where the prompt/cursor and the user's attention already are, and it
         never covers Claude's output or the prompt where the path lands. -->
    <div
      class="pointer-events-none absolute inset-x-0 bottom-0 z-10 flex items-center gap-3 border-t border-border bg-card/95 px-3 py-2 text-sm backdrop-blur"
    >
      {#if uploading > 0}
        <Loader2 class="size-4 shrink-0 animate-spin text-primary" />
        <span class="min-w-0 truncate font-medium" title={uploadLabel}>{uploadLabel}</span>
        <div class="h-1.5 w-28 shrink-0 overflow-hidden rounded-full bg-secondary">
          <div
            class="h-full rounded-full bg-primary transition-[width] duration-200"
            style="width: {Math.max(uploadPct, 4)}%"
          ></div>
        </div>
        <span class="shrink-0 text-xs tabular-nums text-muted-foreground">{uploadPct}%</span>
      {:else if uploadOk}
        <Check class="size-4 shrink-0 text-[var(--color-success)]" />
        <span class="min-w-0 truncate">{uploadNote}</span>
      {:else}
        <TriangleAlert class="size-4 shrink-0 text-destructive" />
        <span class="min-w-0 truncate">{uploadNote}</span>
      {/if}
    </div>
  {/if}

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
