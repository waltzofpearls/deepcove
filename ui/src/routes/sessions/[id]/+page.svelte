<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api/client';
  import { sessions } from '$lib/stores/sessions.svelte';
  import type { Session, SessionHost, SessionPreview } from '$lib/api/types';
  import { Button } from '$lib/components/ui/button';
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
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';
  import Terminal from '$lib/components/session/Terminal.svelte';
  import StatusIndicator from '$lib/components/session/StatusIndicator.svelte';
  import SessionTags from '$lib/components/session/SessionTags.svelte';
  import SessionSettingsDialog from '$lib/components/session/SessionSettingsDialog.svelte';
  import SessionFolderNote from '$lib/components/session/SessionFolderNote.svelte';
  import ModeToggle, { type SessionMode } from '$lib/components/session/ModeToggle.svelte';
  import { createConverter, FULLVIEW_FONT_SIZE } from '$lib/components/session/ansiToHtml';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';
  import Settings from '@lucide/svelte/icons/settings';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import Maximize from '@lucide/svelte/icons/maximize';
  import Minimize from '@lucide/svelte/icons/minimize';
  import Pause from '@lucide/svelte/icons/pause';
  import Pencil from '@lucide/svelte/icons/pencil';
  import Play from '@lucide/svelte/icons/play';
  import Power from '@lucide/svelte/icons/power';
  import Trash from '@lucide/svelte/icons/trash-2';
  import X from '@lucide/svelte/icons/x';

  // page.params.id is typed as string | undefined because SvelteKit
  // can't statically prove the param is present. The route is [id]
  // so it always is at runtime, and we fall back to '' so downstream
  // API signatures expecting `string` typecheck. A missing id would
  // produce a 404 from the server, surfaced via loadError.
  const id = $derived(page.params.id ?? '');

  // The session's host comes from the aggregated list, not the single-session GET (which doesn't
  // carry it). For a remote session every per-session call must route to its owning node over WASM, so
  // we subscribe to the store and learn the host from there. `host` is undefined for a local session
  // (use the boot transport) and for an id not yet seen in the list. `hostKey` is the stable string we
  // gate the loader on, so a fresh list reference each poll doesn't re-fire the fetch.
  $effect(() => sessions.subscribe());
  const listed = $derived(sessions.find(id));
  const host = $derived<SessionHost | undefined>(listed ? sessions.hostFor(listed) : undefined);
  const hostKey = $derived(host?.dns_name ?? '');

  let session = $state<Session | null>(null);
  // loadError is the initial-load failure (no session to show at all).
  // actionError is a transient failure of a start/stop/destroy call
  // while the session is already loaded, surfaced as a dismissable
  // banner so the user knows the action didn't take effect.
  let loadError = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  // Bound to the destroy AlertDialog. Lifting it to script-state
  // keeps the Trash button decoupled from the AlertDialogTrigger
  // (it stays inside a TooltipTrigger like its siblings), so opening
  // the dialog is a plain onclick.
  let destroyOpen = $state(false);
  // Bound to the SessionSettingsDialog, which the cog opens. It writes an updated session back through
  // bind:session, so a folder change lands here without another fetch.
  let settingsOpen = $state(false);

  // Inline rename of the session name in the toolbar. Click the name to
  // edit; Enter (or blur) commits, Esc cancels.
  let editingName = $state(false);
  let nameDraft = $state('');
  let nameInput = $state<HTMLInputElement | undefined>(undefined);

  function startRename() {
    if (!session) return;
    nameDraft = session.name;
    editingName = true;
    // Focus + select after the input renders.
    setTimeout(() => nameInput?.select(), 0);
  }

  async function commitRename() {
    if (!editingName || !session) return;
    const name = nameDraft.trim();
    editingName = false;
    if (!name || name === session.name) return;
    try {
      const updated = await api.renameSession(session.id, name, host);
      session = updated;
      sessions.patch(updated.id, { name: updated.name });
    } catch (e) {
      actionError = `Rename failed: ${e instanceof Error ? e.message : String(e)}`;
    }
  }

  function onNameKey(e: KeyboardEvent) {
    if (e.key === 'Enter') {
      e.preventDefault();
      nameInput?.blur(); // blur commits
    } else if (e.key === 'Escape') {
      e.preventDefault();
      editingName = false; // unmount without saving
    }
  }

  // Mode lives in the URL (?mode=agent|shell|split) so it's
  // shareable / refreshable. Default agent (omitted from URL).
  // currentMode falls back to 'agent' when the URL says 'split'
  // but the viewport is too narrow. This keeps the URL honest while
  // refusing to render a useless side-by-side on phones. Widening
  // the window snaps back to split automatically.
  const mode = $derived<SessionMode>((page.url.searchParams.get('mode') as SessionMode) || 'agent');
  let isWide = $state(true);
  $effect(() => {
    const mq = matchMedia('(min-width: 768px)');
    isWide = mq.matches;
    const onChange = (e: MediaQueryListEvent) => (isWide = e.matches);
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  });
  const currentMode = $derived<SessionMode>(mode === 'split' && !isWide ? 'agent' : mode);

  // Keep the URL honest: if `?mode=split` is opened on a narrow
  // viewport (refresh, deep link, shared URL), rewrite to drop the
  // mode param so the URL matches what's actually rendered. The
  // currentMode fallback above handles the in-render transition;
  // this $effect closes the loop on URL state. No loop risk because
  // the goto drops the param and the guard then short-circuits.
  $effect(() => {
    if (!isWide && page.url.searchParams.get('mode') === 'split') {
      const url = new URL(page.url);
      url.searchParams.delete('mode');
      goto(url.pathname + url.search, {
        replaceState: true,
        keepFocus: true,
        noScroll: true,
      });
    }
  });

  // The shell window lives in the sandbox's tmux, so a rebuilt sandbox has none, while the URL still
  // says Shell. Without this the attach quietly lands on the agent's window instead.
  let shellReady = $state(false);
  $effect(() => {
    const needsShell = currentMode === 'shell' || currentMode === 'split';
    const ready = session?.status === 'running';
    // Re-runs when the sandbox is replaced, since the new one has no shell window either.
    void session?.sandbox_id;
    if (!needsShell || !ready || !session) {
      shellReady = false;
      return;
    }
    let cancelled = false;
    const sess = untrack(() => session);
    const h = untrack(() => host);
    void ensureShellWindow(sess, h).then((ok) => {
      if (!cancelled) shellReady = ok;
    });
    return () => {
      cancelled = true;
    };
  });

  // 409 means it is already there, which is success for us.
  async function ensureShellWindow(
    sess: Session | null,
    h: SessionHost | undefined,
  ): Promise<boolean> {
    if (!sess) return false;
    try {
      await api.createTmuxWindow(sess.id, { name: 'shell' }, h);
      return true;
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) return true;
      actionError = `Failed to open the shell window: ${e instanceof Error ? e.message : String(e)}`;
      return false;
    }
  }

  async function switchMode(next: SessionMode): Promise<void> {
    if (!session) return;
    actionError = null;
    const url = new URL(page.url);
    if (next === 'agent') {
      url.searchParams.delete('mode');
    } else {
      url.searchParams.set('mode', next);
    }
    goto(url.pathname + url.search, {
      replaceState: true,
      keepFocus: true,
      noScroll: true,
    });
  }
  // Cached last preview, so a session that is not running shows a dimmed snapshot of where it got to
  // rather than an empty pane.
  let preview = $state<SessionPreview | null>(null);
  let previewHtml = $state('');
  // $state so the convert effect re-fires when createConverter()
  // resolves. See SessionCard.svelte for the same reasoning.
  let converter = $state<Awaited<ReturnType<typeof createConverter>> | undefined>(undefined);

  // Why there is no terminal, in the user's terms. A stopped session is often on its way back (the
  // node restarts what it was running), so this states the rule instead of ordering a Start.
  const statusHint = $derived(
    session?.status === 'errored'
      ? 'Start it again, or destroy and recreate it.'
      : 'The terminal opens when the session is running.',
  );

  $effect(() => {
    // Deps: id and hostKey. Wait for the first poll to land (sessions.loading flips false) so a remote
    // session's host is known before we fetch, because calling getSession on the local node for a
    // session that lives on a peer would 404. A local session simply has host undefined and loads from
    // this node.
    void hostKey;
    if (sessions.loading) return;
    // host itself is read untracked: hostKey already captures changes that matter, and we don't want a
    // new list reference each poll (same dns_name) to re-fire the fetch.
    const h = untrack(() => host);
    // AbortController, not just a cancelled flag: a leaked /preview fetch
    // here was the root cause of the dashboard "card stuck on 'waiting for
    // first snapshot…'" bug. Navigating back left the request occupying a
    // Firefox connection slot, and the dashboard SessionCard's own /preview
    // for the same id queued behind it indefinitely. See client.ts.
    const ac = new AbortController();
    api
      .getSession(id, h, ac.signal)
      .then((s) => {
        if (!ac.signal.aborted) session = s;
      })
      .catch((e) => {
        if (!ac.signal.aborted) loadError = String(e);
      });
    // Fetch the preview once on mount. The server's cache returns
    // the last good capture even when the session is stopped, so
    // there's something to render in the not-running branch.
    api
      .sessionPreview(id, h, ac.signal)
      .then((p) => {
        if (!ac.signal.aborted) preview = p;
      })
      .catch(() => {});
    return () => {
      ac.abort();
    };
  });

  // Keep the loaded session's status in step with the list poll. The fetch above runs once per id, so
  // without this a deepcove restart (the session goes stopped, then running again) never reaches the
  // view: it keeps rendering a terminal whose stream is long dead.
  $effect(() => {
    const polled = listed;
    if (!polled) return;
    untrack(() => {
      if (session && session.id === polled.id && session.status !== polled.status) {
        session = { ...session, status: polled.status };
      }
    });
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
      await api.startSession(session.id, host);
      session = await api.getSession(session.id, host);
    } catch (e) {
      actionError = `Start failed: ${e instanceof Error ? e.message : String(e)}`;
    }
  }

  async function stop() {
    if (!session) return;
    actionError = null;
    try {
      await api.stopSession(session.id, host);
      session = await api.getSession(session.id, host);
    } catch (e) {
      actionError = `Stop failed: ${e instanceof Error ? e.message : String(e)}`;
    }
  }

  // destroyConfirmed runs after the AlertDialog's Destroy action.
  // bits-ui closes the dialog before the click bubbles, so no manual
  // close is needed: we just clear the actionError and fire the API call.
  //
  // Optimistically remove the session from the shared store before
  // navigating to '/'. Without this, the dashboard mounts with the
  // stale list, the doomed card's preview fetch 404s, and it flashes
  // "waiting for first snapshot…" until the next poll tick prunes
  // it. Next poll still reconciles canonically.
  async function destroyConfirmed() {
    if (!session) return;
    actionError = null;
    try {
      await api.destroySession(session.id, host);
      sessions.remove(session.id);
      goto('/');
    } catch (e) {
      actionError = `Destroy failed: ${e instanceof Error ? e.message : String(e)}`;
    }
  }
</script>

<div class="flex h-full flex-col">
  <!-- Toolbar above the terminal: identity, status, mode toggle,
       actions. Three flex zones with shrink-0 outer groups and a
       centered ModeToggle between two flex-1 spacers. Below md the
       spacers collapse and the gap tightens so a phone-width row
       fits without aggressive truncation of the session name. -->
  <div class="flex items-center gap-2 border-b border-border bg-card px-2 py-2 md:gap-3 md:px-4">
    <div class="flex min-w-0 shrink items-center gap-2 md:gap-3">
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
        <StatusIndicator status={session.status} />
        <div class="min-w-0">
          {#if editingName}
            <input
              bind:this={nameInput}
              bind:value={nameDraft}
              onkeydown={onNameKey}
              onblur={commitRename}
              maxlength="100"
              class="w-44 max-w-full rounded border border-input bg-transparent px-1.5 py-0.5 font-mono text-sm font-medium focus:outline-none focus:ring-1 focus:ring-ring"
            />
          {:else}
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger>
                  {#snippet child({ props })}
                    <button
                      {...props}
                      onclick={startRename}
                      class="group/name flex max-w-full items-center gap-1 font-mono text-sm font-medium"
                    >
                      <span class="truncate">{session?.name}</span>
                      <Pencil
                        class="size-3 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover/name:opacity-100"
                      />
                    </button>
                  {/snippet}
                </TooltipTrigger>
                <TooltipContent>Rename session</TooltipContent>
              </Tooltip>
            </TooltipProvider>
          {/if}
        </div>
        <!-- prefer `listed` (aggregated, carries host) over `session` (single GET, no host) for the badges -->
        <SessionTags session={listed ?? session} elevated />
      {:else if loadError}
        <span class="text-sm text-destructive">{loadError}</span>
      {:else if listed}
        <!-- Canonical session not loaded yet (e.g. awaiting the cross-node sign-in), but the store
             already knows it from the aggregated list, so show its name rather than a bare "loading…". -->
        <span class="truncate font-mono text-sm font-medium">{listed.name}</span>
      {:else}
        <span class="text-sm text-muted-foreground">loading…</span>
      {/if}
    </div>

    <!-- Middle: mode toggle, centered between flex-1 spacers on
         desktop. Spacers collapse below md so the toggle floats
         inline with the name and actions (no wasted whitespace on
         phone). Only visible while the session is attachable. -->
    <div class="hidden flex-1 md:block"></div>
    {#if session?.status === 'running'}
      <ModeToggle mode={currentMode} onChange={switchMode} />
    {/if}
    <div class="hidden flex-1 md:block"></div>

    <div class="flex shrink-0 items-center gap-1">
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
                  <Power class="size-4" />
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
        {#if session}
          <Tooltip>
            <TooltipTrigger>
              {#snippet child({ props })}
                <Button
                  {...props}
                  variant="ghost"
                  size="icon"
                  onclick={() => (settingsOpen = true)}
                  aria-label="Session settings"
                >
                  <Settings class="size-4" />
                </Button>
              {/snippet}
            </TooltipTrigger>
            <TooltipContent>Session settings</TooltipContent>
          </Tooltip>
        {/if}
        <Tooltip>
          <TooltipTrigger>
            {#snippet child({ props })}
              <Button
                {...props}
                variant="ghost"
                size="icon"
                onclick={() => (destroyOpen = true)}
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

  <!-- Edit-mounts sheet. Bind session so the sheet can write the
       updated row back after a successful PUT, which keeps the toolbar
       and the rest of the page in sync without an extra fetch. -->
  {#if session}
    <SessionSettingsDialog bind:open={settingsOpen} bind:session {host} />
  {/if}

  <!-- Destroy confirmation. State-driven open via `destroyOpen` so
       the Trash button can keep its Tooltip wrapper instead of
       nesting two triggers. AlertDialog's primitive blocks any
       interaction outside the dialog while open, a real safety net
       compared to the bare browser confirm() it replaced. -->
  <AlertDialog bind:open={destroyOpen}>
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>Destroy session?</AlertDialogTitle>
        <AlertDialogDescription>
          {#if session}
            This permanently destroys “{session.name}”. This action cannot be undone.
          {:else}
            This permanently destroys the session. This action cannot be undone.
          {/if}
        </AlertDialogDescription>
      </AlertDialogHeader>
      {#if session}
        <SessionFolderNote {session} />
      {/if}
      <AlertDialogFooter>
        <AlertDialogCancel>Cancel</AlertDialogCancel>
        <AlertDialogAction variant="destructive" onclick={destroyConfirmed}>
          Destroy
        </AlertDialogAction>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>

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
       toolbar above is excluded from real fullscreen, matching the
       intent (the terminal alone, edge-to-edge). -->
  <div bind:this={containerEl} class="relative flex-1 overflow-hidden bg-[var(--term-bg)]">
    {#if session?.status === 'running'}
      {#if currentMode === 'split'}
        <!-- Split mode: two side-by-side terminals, each pinned to
             its own tmux window via the ?tmux_window= query param
             (per-client window state is a tmux native). Desktop
             only, since currentMode handles the mobile fallback. -->
        <div class="grid h-full grid-cols-2 divide-x divide-border">
          <Terminal
            sessionId={session.id}
            tmuxWindow="main"
            {host}
            onWorkspaceVM={session.on_workspace_vm}
          />
          {#if shellReady}
            <Terminal
              sessionId={session.id}
              tmuxWindow="shell"
              {host}
              onWorkspaceVM={session.on_workspace_vm}
            />
          {:else}
            <div class="flex h-full items-center justify-center text-sm text-muted-foreground">
              opening shell…
            </div>
          {/if}
        </div>
      {:else if currentMode === 'agent' || shellReady}
        <!-- Single-pane: Agent or Shell. Terminal re-attaches
             reactively when tmuxWindow changes (see step 6). -->
        <Terminal
          sessionId={session.id}
          tmuxWindow={currentMode === 'shell' ? 'shell' : 'main'}
          {host}
          onWorkspaceVM={session.on_workspace_vm}
        />
      {:else}
        <div class="flex h-full items-center justify-center text-sm text-muted-foreground">
          opening shell…
        </div>
      {/if}
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
        {#if session.status === 'errored'}
          <CircleAlert class="size-16 text-destructive" />
        {:else if session.status === 'starting'}
          <Loader2 class="size-16 animate-spin text-muted-foreground" />
        {:else}
          <Pause class="size-16 text-muted-foreground" />
        {/if}
        <span class="text-sm uppercase tracking-wider text-muted-foreground">
          {session.status}
        </span>
        <span class="text-xs text-muted-foreground">{statusHint}</span>
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
