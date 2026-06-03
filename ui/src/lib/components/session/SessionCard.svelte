<script lang="ts">
  import type { Session, SessionPreview } from '$lib/api/types';
  import { api } from '$lib/api/client';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { usePolling } from '$lib/polling';
  import { ageOf, shortId } from '$lib/format';
  import { createConverter, CARD_FONT_SIZE } from './ansiToHtml';
  import StatusIndicator from './StatusIndicator.svelte';
  import {
    DropdownMenu,
    DropdownMenuTrigger,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
  } from '$lib/components/ui/dropdown-menu';
  import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
  } from '$lib/components/ui/alert-dialog';
  import Pause from '@lucide/svelte/icons/pause';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';
  import EllipsisVertical from '@lucide/svelte/icons/ellipsis-vertical';
  import Play from '@lucide/svelte/icons/play';
  import Square from '@lucide/svelte/icons/square';
  import Trash from '@lucide/svelte/icons/trash-2';

  // Card preview uses an unmounted xterm.js instance (via the shared
  // converter module) to render the server-provided ANSI bytes into
  // HTML. Same renderer the full view uses, so colors/bold/wide-chars
  // match without any palette plumbing on our side.
  //
  // The server caches the last successful capture per session, so
  // stopped sessions still return their most recent snapshot — we
  // just dim it and overlay a status icon instead of going black.
  let { session }: { session: Session } = $props();

  let preview = $state<SessionPreview | null>(null);
  let html = $state('');

  // One converter per card; disposed on unmount. We avoid a module
  // singleton so concurrent cards don't race on a shared Terminal.
  // Declared as $state so the "convert when preview changes" effect
  // re-fires the moment the dynamic-import finishes — otherwise a
  // preview that arrives before the converter resolves would leave
  // the card blank until the next poll tick.
  let converter = $state<Awaited<ReturnType<typeof createConverter>> | undefined>(undefined);

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

  // Poll the preview endpoint at the configured cadence. AbortController is
  // required so an in-flight fetch from a briefly-mounted card (e.g. the
  // dashboard render between sessions.add(created) and goto(/sessions/<id>))
  // doesn't leak its connection slot — see client.ts for the pool-exhaustion
  // story.
  $effect(() => {
    const ac = new AbortController();
    const stop = usePolling(
      async () => {
        try {
          preview = await api.sessionPreview(session.id, ac.signal);
        } catch {
          /* aborts and transient failures both land here; next tick retries */
        }
      },
      () => preferences.pollIntervalMs
    );
    return () => {
      ac.abort();
      stop();
    };
  });

  // Convert preview ANSI → HTML whenever either changes. The
  // converter chains internally so back-to-back calls don't
  // interleave on the shared Terminal.
  $effect(() => {
    const p = preview;
    const c = converter;
    if (!p || !p.ansi || !c) {
      html = '';
      return;
    }
    let cancelled = false;
    c.convert(p.ansi, { fontSize: CARD_FONT_SIZE }).then((out) => {
      if (!cancelled) html = out;
    });
    return () => {
      cancelled = true;
    };
  });

  // Quick actions. Stop/start optimistically patch the store so the
  // status indicator flips immediately; the dashboard poll reconciles.
  // Destroy removes the card right away (the doomed session's preview
  // would otherwise 404 until the next tick).
  let destroyOpen = $state(false);

  async function doStop() {
    sessions.patch(session.id, { status: 'stopped' });
    try {
      await api.stopSession(session.id);
    } catch {
      /* next poll reconciles the true status */
    }
  }

  async function doStart() {
    sessions.patch(session.id, { status: 'running' });
    try {
      await api.startSession(session.id);
    } catch {
      /* next poll reconciles */
    }
  }

  async function doDestroy() {
    sessions.remove(session.id);
    try {
      await api.destroySession(session.id);
    } catch {
      /* next poll re-adds it if the destroy didn't take */
    }
  }
</script>

<div
  class="group relative flex aspect-[4/3] flex-col overflow-hidden rounded-lg border border-border bg-card transition-colors hover:border-primary/50"
>
  <!-- Full-card navigation. Above the visual content but below the
       action menu (z-20), so a click anywhere opens the session while
       the menu intercepts its own clicks. Avoids nesting a button
       inside an anchor. -->
  <a
    href="/sessions/{session.id}"
    class="absolute inset-0 z-10"
    aria-label="Open session {session.name}"
  ></a>

  <div class="relative flex-1 overflow-hidden bg-[var(--term-bg)]">
    {#if html}
      <div
        class="ansi-card absolute inset-0 overflow-hidden p-2"
        class:opacity-40={session.status !== 'running'}
        style="white-space: pre; line-height: 1.2;"
      >
        {@html html}
      </div>
    {/if}

    {#if session.status === 'running' && !html}
      <div
        class="absolute inset-0 flex items-center justify-center text-sm text-muted-foreground"
      >
        waiting for first snapshot…
      </div>
    {:else if session.status !== 'running'}
      <div
        class="pointer-events-none absolute inset-0 flex flex-col items-center justify-center gap-1.5"
      >
        {#if session.status === 'errored'}
          <CircleAlert class="size-10 text-destructive" />
        {:else}
          <Pause class="size-10 text-muted-foreground" />
        {/if}
        <span class="text-xs uppercase tracking-wider text-muted-foreground">
          {session.status}
        </span>
      </div>
    {/if}
  </div>

  <div class="flex items-center justify-between gap-2 border-t border-border px-3 py-2">
    <div class="flex min-w-0 items-center gap-2">
      <StatusIndicator status={session.status} />
      <div class="min-w-0">
        <div class="truncate font-mono text-sm font-medium">{session.name}</div>
        <div class="truncate text-xs text-muted-foreground">
          {session.agent} · {shortId(session.id)}
        </div>
      </div>
    </div>
    <div class="flex shrink-0 items-center gap-1.5">
      <span class="text-xs text-muted-foreground">Active {ageOf(session.last_activity_at)} ago</span>
      <!-- relative z-20 raises just the menu above the full-card nav
           link; the rest of the footer stays below it so a click there
           still opens the session. -->
      <div class="relative z-20">
        <DropdownMenu>
          <DropdownMenuTrigger
            aria-label="Session actions"
            class="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
          >
            <EllipsisVertical class="size-4" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" class="w-36">
            {#if session.status === 'running'}
              <DropdownMenuItem onclick={doStop}>
                <Square class="size-4" />
                Stop
              </DropdownMenuItem>
            {:else}
              <DropdownMenuItem onclick={doStart}>
                <Play class="size-4" />
                Start
              </DropdownMenuItem>
            {/if}
            <DropdownMenuSeparator />
            <DropdownMenuItem
              onclick={() => (destroyOpen = true)}
              class="text-destructive data-[highlighted]:text-destructive"
            >
              <Trash class="size-4" />
              Destroy
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </div>
  </div>
</div>

<AlertDialog bind:open={destroyOpen}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>Destroy session?</AlertDialogTitle>
      <AlertDialogDescription>
        This permanently removes “{session.name}” and its container. This can't be
        undone.
      </AlertDialogDescription>
    </AlertDialogHeader>
    <AlertDialogFooter>
      <AlertDialogCancel>Cancel</AlertDialogCancel>
      <AlertDialogAction variant="destructive" onclick={doDestroy}>Destroy</AlertDialogAction>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>

<style>
  /* xterm.js's serializeAsHTML emits a fixed shape:
       <pre>
         <div style='color/bg/font-family/font-size'>
           <div><span>row content (padded with spaces to col width)</span></div>
           ...
         </div>
       </pre>
     Each row is padded with literal spaces out to the Terminal's col
     width. white-space:pre MUST apply at every level — if it
     doesn't, the padding wraps and the card fills with gaps. Set
     globally so {@html}-injected DOM matches regardless of Svelte
     scoping. Font size/family come from the Terminal options so the
     serializer's inline style already has the right values. */
  :global(.ansi-card pre),
  :global(.ansi-card pre *) {
    white-space: pre !important;
    margin: 0 !important;
    padding: 0 !important;
    line-height: 1.2 !important;
  }
</style>
