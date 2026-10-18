<script lang="ts">
  import { untrack } from 'svelte';
  import { api, preflightTimeoutMs } from '$lib/api/client';
  import { usePolling } from '$lib/polling';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { sessions } from '$lib/stores/sessions.svelte';
  import {
    bootsVMs,
    RUNTIME_SANDBOXLESS,
    runtimeLabel,
    runtimeMissingReason,
    runtimeMissingShort,
    runtimeSetUpByDeepcove,
    sharesHostFolders,
  } from '$lib/util/runtime';
  import RuntimeSetupButton from '$lib/components/RuntimeSetupButton.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
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
  import SessionSettingsDialog from '$lib/components/session/SessionSettingsDialog.svelte';
  import { describeVMSize, formatRunningSize } from '$lib/util/vmSize';
  import { folderAwaitingRestart, sizeAwaitingRestart } from '$lib/util/pendingRestart';
  import type {
    NodeInfo,
    RuntimesInfo,
    RuntimeView,
    SandboxView,
    SessionHost,
    Session,
    SessionResources,
    VMSize,
    WorkspaceVM,
  } from '$lib/api/types';
  import Box from '@lucide/svelte/icons/box';
  import ShieldOff from '@lucide/svelte/icons/shield-off';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

  // One node's runtimes and workspace VM. Every call carries `host`, so a section only ever talks to
  // its own node, and an unreachable one fails alone.
  let { node, host }: { node: NodeInfo; host?: SessionHost } = $props();

  // What each runtime on this node is actually hosting, joined against the session table.
  let runtimes = $state<RuntimeView[] | null>(null);
  let error = $state<string | null>(null);

  // This node's workspace VM, or null when it has none. Polled with the runtimes so the card and the
  // sandbox rows never disagree about whether it exists.
  let workspaceVM = $state<WorkspaceVM | null>(null);

  // This node's own VM sizes, which scale with its memory. Asked once per node: they change only with
  // the machine. Keyed on the node's id, since the page hands over a new host object on every poll.
  let nodeRuntimes = $state<RuntimesInfo | null>(null);
  const hostKey = $derived(host?.stable_node_id ?? '');
  $effect(() => {
    void hostKey;
    nodeRuntimes = null;
    // The WASM transport ignores the abort, so a late answer for the last node is dropped here instead.
    let cancelled = false;
    const ac = new AbortController();
    api
      .preflight(
        untrack(() => host),
        ac.signal,
        preflightTimeoutMs,
      )
      .then((p) => {
        if (!cancelled) nodeRuntimes = p.runtimes;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      ac.abort();
    };
  });

  async function load(signal?: AbortSignal) {
    try {
      const [views, vm] = await Promise.all([
        api.runtimes(host, signal),
        api.workspaceVM(host, signal),
      ]);
      runtimes = views.runtimes;
      workspaceVM = vm;
      error = null;
    } catch (e) {
      if (!signal?.aborted) {
        error = e instanceof Error ? e.message : String(e);
        // Stop the spinner. A failure is an answer, and leaving this null shows both at once.
        runtimes ??= [];
      }
    }
  }

  // Creating and editing both go through one dialog, so a form is never sitting under a list of
  // machines looking like it belongs to the last one.
  let editOpen = $state(false);
  let deleteWorkspaceOpen = $state(false);

  // From the session list, not the machine's sandbox: a machine that was never built still has sessions
  // waiting on it, and a delete takes those with it.
  //
  // Scoped to this node: the store holds every node's sessions and this component renders once per
  // node, so an unscoped count warns about the wrong machines and can send "delete the sessions too"
  // for sessions that live somewhere else.
  const workspaceSessions = $derived(
    sessions.list.filter(
      (s) => s.on_workspace_vm && s.host?.stable_node_id === node.stable_node_id,
    ),
  );

  async function deleteWorkspace() {
    deleteWorkspaceOpen = false;
    try {
      await api.deleteWorkspaceVM(workspaceSessions.length > 0, host);
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

  // The session being edited. A session VM hosts exactly one session, so its row edits that one.
  let editingSession = $state<Session | null>(null);
  let settingsOpen = $state(false);

  function editSession(sandbox: SandboxView) {
    editingSession = sessions.list.find((s) => s.id === sandbox.sessions[0]?.id) ?? null;
    if (!editingSession) {
      // The row knows the session's id, the store has not caught up. Better to say so than to swallow
      // the click.
      error = 'That session is still loading. Try again in a moment.';
      return;
    }
    settingsOpen = true;
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
      await api.destroySandbox(target.runtime, target.sandbox.sandbox_id, host);
      await load();
    } catch (e) {
      // The dialog is already gone, so a failure surfaces in the page's error banner instead.
      error = e instanceof Error ? e.message : String(e);
    } finally {
      destroying = false;
    }
  }

  // The workspace VM's row when this node lists no VM for it, in its own runtime's section. Its record
  // can still name a VM that died with a deepcove restart.
  const pendingWorkspaceOn = (view: RuntimeView) =>
    Boolean(workspaceVM) && !workspaceSandbox && workspaceVM?.runtime === view.name;

  // Which kind of VM a row is, which only a VM runtime has: everywhere else the section header already
  // says what it is. Empty means no chip, except for one nobody owns, which is worth saying anywhere.
  function kindOf(sandbox: SandboxView, runtime: string): string {
    if (sandbox.is_workspace_vm) return 'Workspace VM';
    if (sandbox.sessions.length === 0) return 'Unused';
    return bootsVMs(runtime) ? 'Session VM' : '';
  }

  // A sandbox nothing owns. Sorted to the top of its runtime, because it is the only row here that
  // wants action rather than reassurance.
  const leaked = (s: SandboxView) => s.sessions.length === 0 && !s.is_workspace_vm;

  function sortedSandboxes(view: RuntimeView): SandboxView[] {
    return [...view.sandboxes].sort((a, b) => Number(leaked(b)) - Number(leaked(a)));
  }

  // The size that is saved for a sandbox, which is what applies after a restart. A running machine can
  // differ from it, because it booted with whatever was saved at the time.
  function savedResources(s: SandboxView): SessionResources | undefined {
    if (s.is_workspace_vm) return workspaceVM?.resources;
    const id = s.sessions?.[0]?.id;
    const sess = id ? sessions.list.find((x) => x.id === id) : undefined;
    return sess?.resources;
  }

  function sizesFor(s: SandboxView): VMSize[] | undefined {
    return s.is_workspace_vm ? nodeRuntimes?.workspace_vm_sizes : nodeRuntimes?.session_vm_sizes;
  }

  function savedSize(s: SandboxView): string {
    return describeVMSize(savedResources(s), sizesFor(s));
  }

  function sizeOf(s: SandboxView): string {
    return (
      savedSize(s) || (s.resources ? formatRunningSize(s.resources) : '') || 'runtime defaults'
    );
  }

  // What the row says under the name. A session on the machine itself has no size: nothing caps it, so
  // only its age is left.
  function sizeAndAge(s: SandboxView, runtime: string): string {
    const parts: string[] = [];
    if (runtime !== RUNTIME_SANDBOXLESS) {
      parts.push(sizeOf(s));
    }
    if (s.created_at) {
      parts.push(`${ageOf(s.created_at)} old`);
    }
    return parts.join(' · ');
  }

  const sizeWaitsForRestart = (s: SandboxView) =>
    sizeAwaitingRestart(s.resources, savedResources(s), sizesFor(s));
  const folderWaitsForRestart = (s: SandboxView) =>
    Boolean(s.is_workspace_vm && workspaceVM) &&
    folderAwaitingRestart(s.shared_folders, workspaceVM!.host_root, workspaceVM!.guest_root);

  // The VM the restart confirm is about. Paired with its runtime, since the handle alone does not say
  // which runtime to ask.
  let restarting = $state<{ runtime: string; sandbox: SandboxView } | null>(null);
  let restartOpen = $state(false);
  // The sandbox a restart is in flight for, so its row says so and cannot be clicked again.
  let restartingID = $state('');

  function confirmRestart(runtime: string, sandbox: SandboxView) {
    restarting = { runtime, sandbox };
    restartOpen = true;
  }

  async function doRestart() {
    const target = restarting;
    restartOpen = false;
    if (!target) return;
    restartingID = target.sandbox.sandbox_id;
    try {
      await api.restartSandbox(target.runtime, target.sandbox.sandbox_id, host);
      await load();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      restartingID = '';
    }
  }

  const sessionNames = (s: SandboxView) => s.sessions.map((x) => x.name).join(', ');

  // The workspace VM's own VM, when this node lists one.
  const workspaceSandbox = $derived(
    runtimes?.flatMap((v) => v.sandboxes).find((s) => s.is_workspace_vm) ?? null,
  );
</script>

<!-- No label, no chip: a runtime with no VM has no kind to tell apart. -->
{#snippet kindChip(label: string)}
  {#if label}
    <span
      class="shrink-0 rounded bg-sky-500/10 px-1.5 py-0.5 text-xs text-sky-700 ring-1 ring-inset ring-sky-600/25 dark:bg-sky-500/15 dark:text-sky-300 dark:ring-sky-500/30"
    >
      {label}
    </span>
  {/if}
{/snippet}

{#snippet restartButton(runtime: string, sandbox: SandboxView)}
  <Button
    variant="ghost"
    size="sm"
    class="shrink-0"
    disabled={restartingID === sandbox.sandbox_id}
    onclick={() => confirmRestart(runtime, sandbox)}
  >
    {#if restartingID === sandbox.sandbox_id}
      <Loader2 class="size-4 animate-spin" />
      Restarting…
    {:else}
      Restart
    {/if}
  </Button>
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
    {#if runtimes.length === 0 && !error}
      <p class="text-sm text-muted-foreground">Nothing on this machine can run sessions.</p>
    {/if}
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
              {runtimeMissingShort(view.missing)}
            {:else}
              {view.reachable ? (view.version ? view.version : 'reachable') : 'not responding'}
            {/if}
          </div>
        </header>

        {#if view.missing}
          <p class="px-4 py-3 text-sm text-muted-foreground">
            {runtimeMissingReason(view.missing)}
          </p>
          {#if runtimeSetUpByDeepcove(view.missing)}
            <div class="px-4 pb-3">
              <!-- The banner and the New Session dialog read the shared preflight, so it is refreshed too. -->
              <RuntimeSetupButton
                runtime={view.name}
                {host}
                ondone={() => Promise.all([load(), preflight.refresh()])}
              />
            </div>
          {/if}
        {:else if !view.reachable}
          <p class="px-4 py-3 text-sm text-muted-foreground">
            {view.message || "This runtime isn't responding."}
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
                    {describeVMSize(workspaceVM.resources, nodeRuntimes?.workspace_vm_sizes) ||
                      'runtime defaults'}
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
                {:else if bootsVMs(view.name)}
                  <Box class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                {:else if view.name === RUNTIME_SANDBOXLESS}
                  <ShieldOff class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                {/if}
                <div class="min-w-0 flex-1">
                  <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                    {#if sandbox.sessions.length > 0}
                      <!-- Every name, wrapping when they do not fit: the workspace VM can carry a
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
                      <span class="truncate text-sm font-medium text-amber-600 dark:text-amber-500">
                        No session owns this
                      </span>
                    {/if}
                    {@render kindChip(kindOf(sandbox, view.name))}
                    <span
                      class="shrink-0 rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground"
                    >
                      {sandbox.state}
                    </span>
                    {#if sandbox.unresponsive}
                      <span
                        class="shrink-0 rounded bg-amber-500/10 px-1.5 py-0.5 text-xs text-amber-700 ring-1 ring-inset ring-amber-600/25 dark:bg-amber-500/15 dark:text-amber-300 dark:ring-amber-500/30"
                      >
                        not responding
                      </span>
                    {/if}
                  </div>
                  <div class="truncate font-mono text-xs text-muted-foreground">
                    {shortId(sandbox.sandbox_id, 24)}
                  </div>
                  {#each sandbox.shared_folders ?? [] as folder (folder.guest_path)}
                    <div class="truncate font-mono text-xs text-muted-foreground">
                      {folder.host_path} → {folder.guest_path}{folder.read_only
                        ? ' (read only)'
                        : ''}
                    </div>
                  {/each}
                  <div class="mt-0.5 text-xs text-muted-foreground">
                    {sizeAndAge(sandbox, view.name)}
                  </div>
                  {#if sandbox.unresponsive}
                    <div class="mt-0.5 text-xs text-amber-600 dark:text-amber-400">
                      The machine is up but not answering. Restart brings it back.
                    </div>
                  {/if}
                  {#if sizeWaitsForRestart(sandbox) || folderWaitsForRestart(sandbox)}
                    <div class="mt-0.5 text-xs text-amber-600 dark:text-amber-400">
                      {#if sizeWaitsForRestart(sandbox)}
                        Running with {formatRunningSize(sandbox.resources ?? {})}.
                      {/if}
                      {#if folderWaitsForRestart(sandbox)}
                        Still sharing {sandbox.shared_folders?.[0]?.host_path}.
                      {/if}
                      Restart to apply.
                    </div>
                  {/if}
                  {#if sandbox.image}
                    <div class="truncate text-xs text-muted-foreground">{sandbox.image}</div>
                  {/if}
                </div>
                {#if sandbox.is_workspace_vm}
                  {#if bootsVMs(view.name)}
                    {@render restartButton(view.name, sandbox)}
                  {/if}
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
                {:else}
                  {#if bootsVMs(view.name)}
                    {@render restartButton(view.name, sandbox)}
                  {/if}
                  {#if sharesHostFolders(view.name)}
                    <Button
                      variant="ghost"
                      size="sm"
                      class="shrink-0"
                      disabled={!sessions.list.some((s) => s.id === sandbox.sessions[0]?.id)}
                      onclick={() => editSession(sandbox)}
                    >
                      Edit
                    </Button>
                  {/if}
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
        {#if bootsVMs(view.name) && !view.missing && view.reachable && !workspaceVM}
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

<AlertDialog bind:open={restartOpen}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>Restart this VM?</AlertDialogTitle>
      <AlertDialogDescription>
        {#if restarting && restarting.sandbox.sessions.length > 0}
          This stops {sessionNames(restarting.sandbox)}, then starts the VM again with its saved
          settings. Your files are kept. Anything running inside stops.
        {:else}
          This stops and starts the VM again with its saved settings. Your files are kept.
        {/if}
      </AlertDialogDescription>
    </AlertDialogHeader>
    <AlertDialogFooter>
      <AlertDialogCancel>Cancel</AlertDialogCancel>
      <AlertDialogAction onclick={doRestart}>Restart</AlertDialogAction>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>

<WorkspaceVMDialog bind:open={editOpen} vm={workspaceVM} {host} onsaved={() => load()} />

{#if editingSession}
  <SessionSettingsDialog
    bind:open={settingsOpen}
    bind:session={editingSession}
    {host}
    onsaved={() => load()}
  />
{/if}

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
