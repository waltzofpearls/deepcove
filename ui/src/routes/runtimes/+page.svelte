<script lang="ts">
  import { api } from '$lib/api/client';
  import { usePolling } from '$lib/polling';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { runtimeLabel, runtimeMissingReason } from '$lib/util/runtime';
  import RuntimeSetupButton from '$lib/components/RuntimeSetupButton.svelte';
  import { ageOf, shortId } from '$lib/format';
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
  import WorkspaceVMDialog from '$lib/components/session/WorkspaceVMDialog.svelte';
  import { formatResources } from '$lib/util/resources';
  import type { RuntimeView, SandboxView, WorkspaceVM } from '$lib/api/types';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Box from '@lucide/svelte/icons/box';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

  // What each runtime on THIS node is actually hosting, joined against the session table. Deliberately
  // not fanned out across the tailnet: which machine a sandbox runs on is the whole question here, and
  // a VM on another node is that node's to show.
  let runtimes = $state<RuntimeView[] | null>(null);
  let error = $state<string | null>(null);

  // This node's shared machine, or null when it has none. Polled with the runtimes so the card and the
  // sandbox rows never disagree about whether it exists.
  let workspaceVM = $state<WorkspaceVM | null>(null);

  async function load(signal?: AbortSignal) {
    try {
      const [views, vm] = await Promise.all([
        api.runtimes(signal),
        api.workspaceVM(undefined, signal),
      ]);
      runtimes = views.runtimes;
      workspaceVM = vm;
      error = null;
    } catch (e) {
      if (!signal?.aborted) error = e instanceof Error ? e.message : String(e);
    }
  }

  // Creating and editing both go through one dialog, so a form is never sitting under a list of
  // machines looking like it belongs to the last one.
  let editOpen = $state(false);
  let deleteWorkspaceOpen = $state(false);

  // From the session list, not the machine's sandbox: a machine that was never built still has sessions
  // waiting on it, and a delete takes those with it.
  const workspaceSessions = $derived(sessions.list.filter((s) => s.on_workspace_vm));

  async function deleteWorkspace() {
    deleteWorkspaceOpen = false;
    try {
      await api.deleteWorkspaceVM(workspaceSessions.length > 0);
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  // The delete confirm needs the session list, which only polls while something subscribes.
  $effect(() => sessions.subscribe());

  $effect(() => {
    const ac = new AbortController();
    const stop = usePolling(
      () => load(ac.signal),
      () => preferences.pollIntervalMs,
    );
    return () => {
      ac.abort();
      stop();
    };
  });

  let refreshing = $state(false);
  async function refreshNow() {
    refreshing = true;
    try {
      await load();
    } finally {
      refreshing = false;
    }
  }

  // The sandbox the destroy confirm is about. Held as a pair because the handle alone does not say
  // which runtime to ask.
  let pending = $state<{ runtime: string; sandbox: SandboxView } | null>(null);
  let destroyOpen = $state(false);
  let destroying = $state(false);

  function confirmDestroy(runtime: string, sandbox: SandboxView) {
    pending = { runtime, sandbox };
    destroyOpen = true;
  }

  async function doDestroy() {
    const target = pending;
    if (!target || destroying) return;
    destroying = true;
    // Close BEFORE awaiting. This page stays mounted either way, unlike the session card (which removes
    // itself) or the session view (which navigates away), so nothing else would ever take the dialog
    // down. Leaving it up for the round trip is what made it look stuck and un-dismissable.
    destroyOpen = false;
    try {
      await api.destroySandbox(target.runtime, target.sandbox.sandbox_id);
      await load();
    } catch (e) {
      // The dialog is already gone, so a failure surfaces in the page's error banner instead.
      error = e instanceof Error ? e.message : String(e);
    } finally {
      destroying = false;
    }
  }

  // The machine is recorded on the node's default runtime, so that is the only section it can show up
  // in before it has been built.
  const pendingWorkspaceOn = (view: RuntimeView) =>
    view.default && Boolean(workspaceVM) && !workspaceVM?.sandbox_id;

  // What a row is, said in the row rather than implied by where it sits.
  function kindOf(sandbox: SandboxView): string {
    if (sandbox.is_workspace_vm) return 'Workspace VM';
    if (sandbox.sessions.length === 0) return 'Unused';
    return 'Private VM';
  }

  // A sandbox nothing owns. Sorted to the top of its runtime, because it is the only row here that
  // wants action rather than reassurance.
  const leaked = (s: SandboxView) => s.sessions.length === 0 && !s.is_workspace_vm;

  function sortedSandboxes(view: RuntimeView): SandboxView[] {
    return [...view.sandboxes].sort((a, b) => Number(leaked(b)) - Number(leaked(a)));
  }

  // What a sandbox was given. The driver's own figures win when it reports them, since those are what
  // the sandbox actually got rather than what was asked for.
  function sizeOf(s: SandboxView): string {
    return formatResources(s.resources) || 'runtime defaults';
  }
</script>

{#snippet kindChip(label: string)}
  <span
    class="shrink-0 rounded bg-sky-500/10 px-1.5 py-0.5 text-xs text-sky-700 ring-1 ring-inset ring-sky-600/25 dark:bg-sky-500/15 dark:text-sky-300 dark:ring-sky-500/30"
  >
    {label}
  </span>
{/snippet}

{#snippet workspaceActions()}
  <Button variant="ghost" size="sm" class="shrink-0" onclick={() => (editOpen = true)}>Edit</Button>
  <Button
    variant="ghost"
    size="icon"
    class="shrink-0 text-muted-foreground hover:text-destructive"
    onclick={() => (deleteWorkspaceOpen = true)}
    aria-label="Delete the workspace VM"
  >
    <Trash2 class="size-4" />
  </Button>
{/snippet}

<div class="mx-auto max-w-5xl p-6">
  <div class="mb-1 flex items-center gap-3">
    <a
      href="/"
      class="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
      aria-label="Back to dashboard"
    >
      <ArrowLeft class="size-4" />
    </a>
    <h1 class="text-lg font-semibold">Runtimes</h1>
    <div class="flex-1"></div>
    <Button
      variant="ghost"
      size="icon"
      onclick={refreshNow}
      disabled={refreshing}
      aria-label="Refresh"
    >
      <RefreshCw class="size-4 {refreshing ? 'animate-spin' : ''}" />
    </Button>
  </div>
  <p class="mb-6 pl-11 text-xs text-muted-foreground">
    What this machine is actually hosting, straight from each runtime. Virtual machines run inside
    deepcove itself, so this is the only place they show up.
  </p>

  {#if error}
    <div
      class="mb-4 rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive"
    >
      {error}
    </div>
  {/if}

  {#if runtimes === null}
    <div class="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 class="size-4 animate-spin" /> Loading…
    </div>
  {:else}
    <div class="space-y-4">
      {#each runtimes as view (view.name)}
        <section class="overflow-hidden rounded-xl border border-border bg-card">
          <header class="flex items-center gap-2 border-b border-border px-4 py-3">
            <h2 class="text-sm font-semibold">{runtimeLabel(view.name)}</h2>
            {#if view.default}
              <span class="rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground">
                default
              </span>
            {/if}
            <div class="flex-1"></div>
            <div class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
              <span
                class="size-2 rounded-full"
                class:bg-green-500={view.reachable}
                class:bg-muted-foreground={view.missing}
                class:bg-destructive={!view.reachable && !view.missing}
                aria-hidden="true"
              ></span>
              {#if view.missing}
                not set up
              {:else}
                {view.reachable ? (view.version ? view.version : 'reachable') : 'unreachable'}
              {/if}
            </div>
          </header>

          {#if view.missing}
            <p class="px-4 pt-3 text-sm text-muted-foreground">
              {runtimeMissingReason(view.missing)}
            </p>
            <div class="px-4 pb-3 pt-2">
              <RuntimeSetupButton runtime={view.name} ondone={() => load()} />
            </div>
          {:else if !view.reachable}
            <p class="px-4 py-3 text-sm text-muted-foreground">
              {view.message || "This runtime's backend isn't reachable."}
            </p>
          {:else if !view.list_supported}
            <p class="px-4 py-3 text-sm text-muted-foreground">
              This runtime can't list what it's hosting.
            </p>
          {:else if view.list_error}
            <p class="px-4 py-3 text-sm text-destructive">
              Couldn't read what this runtime is hosting: {view.list_error}
            </p>
          {:else if view.sandboxes.length === 0 && view.orphaned.length === 0 && !pendingWorkspaceOn(view)}
            <p class="px-4 py-3 text-sm text-muted-foreground">Nothing running.</p>
          {:else}
            <ul class="divide-y divide-border">
              <!-- Recorded but never built: the same row as a running machine, so it reads as one. -->
              {#if pendingWorkspaceOn(view) && workspaceVM}
                <li class="flex items-start gap-3 px-4 py-3">
                  <Box class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <div class="min-w-0 flex-1">
                    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <span class="truncate text-sm text-muted-foreground">no sessions yet</span>
                      {@render kindChip('Workspace VM')}
                      <span
                        class="shrink-0 rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground"
                      >
                        not started
                      </span>
                    </div>
                    <div class="truncate font-mono text-xs text-muted-foreground">
                      {workspaceVM.host_root} → {workspaceVM.guest_root}
                    </div>
                    <div class="mt-0.5 text-xs text-muted-foreground">
                      {formatResources(workspaceVM.resources) || 'runtime defaults'}
                    </div>
                    {#if workspaceVM.image}
                      <div class="truncate text-xs text-muted-foreground">{workspaceVM.image}</div>
                    {/if}
                    <div class="mt-0.5 text-xs text-muted-foreground">
                      Starts with your first session on it.
                    </div>
                  </div>
                  {@render workspaceActions()}
                </li>
              {/if}
              {#each sortedSandboxes(view) as sandbox (sandbox.sandbox_id)}
                <li class="flex items-start gap-3 px-4 py-3">
                  {#if leaked(sandbox)}
                    <TriangleAlert class="mt-0.5 size-4 shrink-0 text-amber-500" />
                  {:else}
                    <Box class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  {/if}
                  <div class="min-w-0 flex-1">
                    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                      {#if sandbox.sessions.length > 0}
                        <!-- Every name, wrapping when they do not fit: a shared machine can carry a
                             dozen, and hiding some would leave nowhere to see them. -->
                        <span
                          class="flex min-w-0 flex-wrap items-center gap-x-1.5 text-sm font-medium"
                        >
                          {#each sandbox.sessions as session, i (session.id)}
                            <a href="/sessions/{session.id}" class="truncate hover:underline">
                              {session.name}
                            </a>
                            {#if i < sandbox.sessions.length - 1}
                              <span class="text-muted-foreground">·</span>
                            {/if}
                          {/each}
                        </span>
                      {:else if sandbox.is_workspace_vm}
                        <span class="truncate text-sm text-muted-foreground">no sessions yet</span>
                      {:else}
                        <span
                          class="truncate text-sm font-medium text-amber-600 dark:text-amber-500"
                        >
                          No session owns this
                        </span>
                      {/if}
                      {@render kindChip(kindOf(sandbox))}
                      <span
                        class="shrink-0 rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground"
                      >
                        {sandbox.state}
                      </span>
                    </div>
                    <div class="truncate font-mono text-xs text-muted-foreground">
                      {shortId(sandbox.sandbox_id, 24)}
                    </div>
                    {#each sandbox.mounts ?? [] as mount (mount.guest_path)}
                      <div class="truncate font-mono text-xs text-muted-foreground">
                        {mount.host_path} → {mount.guest_path}{mount.read_only
                          ? ' (read only)'
                          : ''}
                      </div>
                    {/each}
                    <div class="mt-0.5 text-xs text-muted-foreground">
                      {sizeOf(sandbox)}
                      {#if sandbox.created_at}
                        · {ageOf(sandbox.created_at)} old
                      {/if}
                    </div>
                    {#if sandbox.image}
                      <div class="truncate text-xs text-muted-foreground">{sandbox.image}</div>
                    {/if}
                  </div>
                  {#if sandbox.is_workspace_vm}
                    {@render workspaceActions()}
                  {:else if leaked(sandbox)}
                    <Button
                      variant="ghost"
                      size="icon"
                      class="shrink-0 text-muted-foreground hover:text-destructive"
                      onclick={() => confirmDestroy(view.name, sandbox)}
                      aria-label="Destroy this sandbox"
                    >
                      <Trash2 class="size-4" />
                    </Button>
                  {/if}
                </li>
              {/each}

              {#each view.orphaned as orphan (orphan.session_id)}
                <li class="flex items-start gap-3 px-4 py-3">
                  <TriangleAlert class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  <div class="min-w-0 flex-1">
                    <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <a
                        href="/sessions/{orphan.session_id}"
                        class="truncate text-sm font-medium hover:underline"
                      >
                        {orphan.session_name}
                      </a>
                      {@render kindChip('Orphaned session')}
                    </div>
                    <div class="text-xs text-muted-foreground">
                      This session says it is running, but this runtime has no machine for it.
                      Stopping and starting it rebuilds one.
                    </div>
                  </div>
                </li>
              {/each}
            </ul>
          {/if}

          <!-- Only when it has something to say: an empty footer is a divider under the last row. -->
          {#if view.default && !view.missing && view.reachable && !workspaceVM}
            <div class="border-t border-border px-4 py-3">
              <p class="text-sm text-muted-foreground">
                No workspace VM. One VM your sessions share, each working in its own folder.
              </p>
              <Button size="sm" class="mt-2" onclick={() => (editOpen = true)}>
                Create a workspace VM
              </Button>
            </div>
          {/if}
        </section>
      {/each}
    </div>
  {/if}
</div>

<AlertDialog bind:open={destroyOpen}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>Destroy this sandbox?</AlertDialogTitle>
      <AlertDialogDescription>
        Nothing owns {pending ? shortId(pending.sandbox.sandbox_id, 24) : 'this sandbox'}, so
        destroying it frees what it's holding. This can't be undone.
      </AlertDialogDescription>
    </AlertDialogHeader>
    <AlertDialogFooter>
      <AlertDialogCancel>Cancel</AlertDialogCancel>
      <AlertDialogAction variant="destructive" onclick={doDestroy}>Destroy</AlertDialogAction>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>

<WorkspaceVMDialog bind:open={editOpen} vm={workspaceVM} onsaved={() => load()} />

<AlertDialog bind:open={deleteWorkspaceOpen}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>Delete the workspace VM?</AlertDialogTitle>
      <AlertDialogDescription>
        {#if workspaceSessions.length > 0}
          {workspaceSessions.length === 1
            ? '1 session lives'
            : `${workspaceSessions.length} sessions live`} on this VM and will go with it: {workspaceSessions
            .map((s) => s.name)
            .join(', ')}. Their agents' conversations are on the VM and will be lost. Your code is
          untouched: it lives in {workspaceVM?.host_root} on this machine.
        {:else}
          The VM and its home will be removed. Your code is untouched: it lives in
          {workspaceVM?.host_root} on this machine.
        {/if}
      </AlertDialogDescription>
    </AlertDialogHeader>
    <AlertDialogFooter>
      <AlertDialogCancel>Cancel</AlertDialogCancel>
      <AlertDialogAction variant="destructive" onclick={deleteWorkspace}>
        {workspaceSessions.length > 0
          ? `Delete the VM and ${workspaceSessions.length} sessions`
          : 'Delete'}
      </AlertDialogAction>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>
