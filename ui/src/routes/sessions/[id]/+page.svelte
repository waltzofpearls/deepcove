<script lang="ts">
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api } from '$lib/api/client';
  import type { Session, SessionPreview } from '$lib/api/types';
  import { Button } from '$lib/components/ui/button';
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';
  import Terminal from '$lib/components/session/Terminal.svelte';
  import StatusDot from '$lib/components/session/StatusDot.svelte';
  import { createConverter, FULLVIEW_FONT_SIZE } from '$lib/components/session/ansiToHtml';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Maximize from '@lucide/svelte/icons/maximize';
  import Minimize from '@lucide/svelte/icons/minimize';
  import Pause from '@lucide/svelte/icons/pause';
  import Play from '@lucide/svelte/icons/play';
  import Square from '@lucide/svelte/icons/square';
  import Trash from '@lucide/svelte/icons/trash-2';
  import X from '@lucide/svelte/icons/x';

  // page.params.id is typed as string | undefined because SvelteKit
  // can't statically prove the param is present. The route is [id]
  // so it always is at runtime — fall back to '' so downstream API
  // signatures expecting `string` typecheck. A missing id would
  // produce a 404 from the server, surfaced via loadError.
  const id = $derived(page.params.id ?? '');

  let session = $state<Session | null>(null);
  // loadError is the initial-load failure (no session to show at all).
  // actionError is a transient failure of a start/stop/destroy call
  // while the session is already loaded — surfaced as a dismissable
  // banner so the user knows the action didn't take effect.
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  // Cached last preview — used to show a dimmed snapshot + Stopped
  // overlay when the session isn't running, instead of a bare
  // "attach is unavailable" message.
  let preview = $state<SessionPreview | null>(null);
  let previewHtml = $state('');
  // $state so the convert effect re-fires when createConverter()
  // resolves — see SessionCard.svelte for the same reasoning.
  let converter = $state<Awaited<ReturnType<typeof createConverter>> | undefined>(undefined);

  $effect(() => {
    let cancelled = false;
    api
      .getSession(id)
      .then((s) => {
        if (!cancelled) session = s;
      })
      .catch((e) => {
        if (!cancelled) loadError = String(e);
      });
    // Fetch the preview once on mount — the server's cache returns
    // the last good capture even when the session is stopped, so
    // there's something to render in the not-running branch.
    api
      .sessionPreview(id)
      .then((p) => {
        if (!cancelled) preview = p;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  });

  $effect(() => {
    let cancelled = false;
    createConverter().then((c) => {
      if (cancelled) {
        c.dispose();
        return;
      }
      converter = c;
    });
    return () => {
      cancelled = true;
      converter?.dispose();
      converter = undefined;
    };
  });

  $effect(() => {
    const p = preview;
    const c = converter;
    if (!p || !p.ansi || !c) {
      previewHtml = '';
      return;
    }
    let cancelled = false;
    c.convert(p.ansi, { fontSize: FULLVIEW_FONT_SIZE }).then((out) => {
      if (!cancelled) previewHtml = out;
    });
    return () => {
      cancelled = true;
    };
  });

  // Fullscreen state mirrors document.fullscreenElement so the button
  // icon flips correctly when ESC is pressed (which leaves fullscreen
  // without going through our toggle).
  let containerEl: HTMLDivElement;
  let isFullscreen = $state(false);

  function toggleFullscreen() {
    if (!containerEl) return;
    if (document.fullscreenElement === containerEl) {
      document.exitFullscreen().catch(() => {});
    } else {
      containerEl.requestFullscreen().catch(() => {});
    }
  }

  $effect(() => {
    function onChange() {
      isFullscreen = document.fullscreenElement === containerEl;
    }
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  });

  async function start() {
    if (!session) return;
    actionError = null;
    try {
      await api.startSession(session.id);
      session = await api.getSession(session.id);
    } catch (e) {
      actionError = `Start failed: ${e instanceof Error ? e.message : String(e)}`;
    }
  }

  async function stop() {
    if (!session) return;
    actionError = null;
    try {
      await api.stopSession(session.id);
      session = await api.getSession(session.id);
    } catch (e) {
      actionError = `Stop failed: ${e instanceof Error ? e.message : String(e)}`;
    }
  }

  async function destroy() {
    if (!session) return;
    if (!confirm(`Destroy session "${session.name}"? This is irreversible.`)) return;
    actionError = null;
    try {
      await api.destroySession(session.id);
      goto('/');
    } catch (e) {
      actionError = `Destroy failed: ${e instanceof Error ? e.message : String(e)}`;
    }
  }
</script>

<div class="flex h-full flex-col">
  <!-- Toolbar above the terminal: identity, status, actions. -->
  <div class="flex items-center justify-between gap-3 border-b border-border bg-card px-4 py-2">
    <div class="flex min-w-0 items-center gap-3">
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger>
            {#snippet child({ props })}
              <Button
                {...props}
                variant="ghost"
                size="icon"
                href="/"
                aria-label="Back to dashboard"
              >
                <ArrowLeft class="size-4" />
              </Button>
            {/snippet}
          </TooltipTrigger>
          <TooltipContent>Back to dashboard</TooltipContent>
        </Tooltip>
      </TooltipProvider>
      {#if session}
        <StatusDot status={session.status} />
        <div class="min-w-0">
          <div class="truncate font-mono text-sm font-medium">{session.name}</div>
          <div class="truncate text-xs text-muted-foreground">
            {session.agent} · {session.id.slice(0, 12)}
          </div>
        </div>
      {:else if loadError}
        <span class="text-sm text-destructive">{loadError}</span>
      {:else}
        <span class="text-sm text-muted-foreground">loading…</span>
      {/if}
    </div>
    <div class="flex items-center gap-1">
      <TooltipProvider>
        {#if session?.status === 'running'}
          <Tooltip>
            <TooltipTrigger>
              {#snippet child({ props })}
                <Button
                  {...props}
                  variant="ghost"
                  size="icon"
                  onclick={stop}
                  aria-label="Stop session"
                >
                  <Square class="size-4" />
                </Button>
              {/snippet}
            </TooltipTrigger>
            <TooltipContent>Stop session</TooltipContent>
          </Tooltip>
        {:else if session?.status === 'stopped'}
          <Tooltip>
            <TooltipTrigger>
              {#snippet child({ props })}
                <Button
                  {...props}
                  variant="ghost"
                  size="icon"
                  onclick={start}
                  aria-label="Start session"
                >
                  <Play class="size-4" />
                </Button>
              {/snippet}
            </TooltipTrigger>
            <TooltipContent>Start session</TooltipContent>
          </Tooltip>
        {/if}
        <Tooltip>
          <TooltipTrigger>
            {#snippet child({ props })}
              <Button
                {...props}
                variant="ghost"
                size="icon"
                onclick={destroy}
                aria-label="Destroy session"
              >
                <Trash class="size-4" />
              </Button>
            {/snippet}
          </TooltipTrigger>
          <TooltipContent>Destroy session</TooltipContent>
        </Tooltip>
        <Tooltip>
          <TooltipTrigger>
            {#snippet child({ props })}
              <Button
                {...props}
                variant="ghost"
                size="icon"
                onclick={toggleFullscreen}
                aria-label={isFullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
              >
                {#if isFullscreen}
                  <Minimize class="size-4" />
                {:else}
                  <Maximize class="size-4" />
                {/if}
              </Button>
            {/snippet}
          </TooltipTrigger>
          <TooltipContent>{isFullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
  </div>

  <!-- Transient action-error banner. Action failures (start/stop/
       destroy) used to be swallowed because the only error region
       was the session-name placeholder, which only renders when the
       session itself isn't loaded. -->
  {#if actionError}
    <div
      class="flex items-center justify-between gap-2 border-b border-border bg-destructive/10 px-4 py-2 text-sm text-destructive"
      role="alert"
    >
      <span class="truncate">{actionError}</span>
      <button
        class="rounded p-1 hover:bg-destructive/10"
        onclick={() => (actionError = null)}
        aria-label="Dismiss error"
      >
        <X class="size-4" />
      </button>
    </div>
  {/if}

  <!-- Terminal pane. requestFullscreen() targets this div, so the
       toolbar above is excluded from real fullscreen — matching the
       intent (the terminal alone, edge-to-edge). -->
  <div bind:this={containerEl} class="relative flex-1 overflow-hidden bg-[var(--term-bg)]">
    {#if session?.status === 'running'}
      <Terminal sessionId={session.id} />
    {:else if session}
      <!-- Not running: render the last cached preview dimmed, with
           a centered status icon. Same UX as the card. -->
      {#if previewHtml}
        <div
          class="ansi-fullview absolute inset-0 overflow-auto p-4 opacity-40"
          style="white-space: pre; line-height: 1.2;"
        >
          {@html previewHtml}
        </div>
      {/if}
      <div
        class="pointer-events-none absolute inset-0 flex flex-col items-center justify-center gap-2"
      >
        <Pause class="size-16 text-muted-foreground" />
        <span class="text-sm uppercase tracking-wider text-muted-foreground">
          {session.status}
        </span>
        <span class="text-xs text-muted-foreground">attach is unavailable</span>
      </div>
    {/if}
  </div>
</div>

<style>
  /* See SessionCard.svelte. Selector is fully global because the
     HTML is injected via {@html} and we want zero scoping risk. */
  :global(.ansi-fullview pre),
  :global(.ansi-fullview pre *) {
    white-space: pre !important;
    margin: 0 !important;
    padding: 0 !important;
    line-height: 1.2 !important;
  }
</style>
