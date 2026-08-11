<script lang="ts">
  import { api } from '$lib/api/client';
  import { usePolling } from '$lib/polling';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { runtimeLabel, runtimeMissingReason } from '$lib/util/runtime';
  import RuntimeSetupButton from '$lib/components/RuntimeSetupButton.svelte';
  import { formatResources } from '$lib/util/resources';
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
  import type { RuntimeView, SandboxView } from '$lib/api/types';
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

  async function load(signal?: AbortSignal) {
    try {
      runtimes = (await api.runtimes(signal)).runtimes;
      error = null;
    } catch (e) {
      if (!signal?.aborted) error = e instanceof Error ? e.message : String(e);
    }
  }

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
      await api.destroySandbox(target.runtime, target.sandbox.handle);
      await load();
    } catch (e) {
      // The dialog is already gone, so a failure surfaces in the page's error banner instead.
      error = e instanceof Error ? e.message : String(e);
    } finally {
      destroying = false;
    }
  }

  // A sandbox nothing owns. Sorted to the top of its runtime, because it is the only row here that
  // wants action rather than reassurance.
  const leaked = (s: SandboxView) => !s.session_id;

  function sortedSandboxes(view: RuntimeView): SandboxView[] {
    return [...view.sandboxes].sort((a, b) => Number(leaked(b)) - Number(leaked(a)));
  }

  // What a sandbox was given. The driver's own figures win when it reports them, since those are what
  // the sandbox actually got rather than what was asked for.
  function sizeOf(s: SandboxView): string {
    return formatResources(s.resources) || 'runtime defaults';
  }
</script>

<div class="mx-auto max-w-2xl p-6">
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
          {:else if view.sandboxes.length === 0 && view.orphaned.length === 0}
            <p class="px-4 py-3 text-sm text-muted-foreground">Nothing running.</p>
          {:else}
            <ul class="divide-y divide-border">
              {#each sortedSandboxes(view) as sandbox (sandbox.handle)}
                <li class="flex items-start gap-3 px-4 py-3">
                  {#if leaked(sandbox)}
                    <TriangleAlert class="mt-0.5 size-4 shrink-0 text-amber-500" />
                  {:else}
                    <Box class="mt-0.5 size-4 shrink-0 text-muted-foreground" />
                  {/if}
                  <div class="min-w-0 flex-1">
                    <div class="flex items-center gap-2">
                      {#if sandbox.session_id}
                        <a
                          href="/sessions/{sandbox.session_id}"
                          class="truncate text-sm font-medium hover:underline"
                        >
                          {sandbox.session_name}
                        </a>
                      {:else}
                        <span
                          class="truncate text-sm font-medium text-amber-600 dark:text-amber-500"
                        >
                          No session owns this
                        </span>
                      {/if}
                      <span
                        class="shrink-0 rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground"
                      >
                        {sandbox.state}
                      </span>
                    </div>
                    <div class="truncate font-mono text-xs text-muted-foreground">
                      {shortId(sandbox.handle, 24)}
                    </div>
                    <div class="mt-0.5 text-xs text-muted-foreground">
                      {sizeOf(sandbox)}
                      {#if sandbox.image}
                        · {sandbox.image}
                      {/if}
                      {#if sandbox.created_at}
                        · {ageOf(sandbox.created_at)} old
                      {/if}
                    </div>
                  </div>
                  {#if leaked(sandbox)}
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
                    <a
                      href="/sessions/{orphan.session_id}"
                      class="truncate text-sm font-medium hover:underline"
                    >
                      {orphan.session_name}
                    </a>
                    <div class="text-xs text-muted-foreground">
                      Session is {orphan.status}, but this runtime no longer has its sandbox. Delete
                      and recreate it.
                    </div>
                  </div>
                </li>
              {/each}
            </ul>
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
        Nothing owns {pending ? shortId(pending.sandbox.handle, 24) : 'this sandbox'}, so destroying
        it frees what it's holding. This can't be undone.
      </AlertDialogDescription>
    </AlertDialogHeader>
    <AlertDialogFooter>
      <AlertDialogCancel>Cancel</AlertDialogCancel>
      <AlertDialogAction variant="destructive" onclick={doDestroy}>Destroy</AlertDialogAction>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>
