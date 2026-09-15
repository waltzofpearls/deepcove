<script lang="ts">
  import { page } from '$app/state';
  import { api } from '$lib/api/client';
  import { usePolling } from '$lib/polling';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { discovery } from '$lib/stores/discovery.svelte';
  import { discoveryRequired } from '$lib/api/transport';
  import { nodeHost } from '$lib/api/sessions';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { machineName } from '$lib/util/tailnet';
  import NodeRuntimes from '$lib/components/nodes/NodeRuntimes.svelte';
  import { Button } from '$lib/components/ui/button';
  import type { NodeInfo } from '$lib/api/types';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';

  // One machine, addressed by the id that survives a logout: everything on this page is about it.
  const nodeID = $derived(page.params.id ?? '');

  let nodes = $state<NodeInfo[] | null>(null);
  let refreshing = $state(false);

  async function loadNodes() {
    try {
      nodes = discoveryRequired() ? discovery.nodes : await api.nodes();
    } catch {
      nodes = [];
    }
  }

  // Polled, so a machine coming back online, or a runtime finishing its setup, shows up here without
  // being asked. The runtimes below poll themselves.
  $effect(() => {
    void loadNodes();
    return usePolling(loadNodes, () => preferences.pollIntervalMs);
  });

  $effect(() => sessions.subscribe());

  const node = $derived(nodes?.find((n) => n.stable_node_id === nodeID) ?? null);
  const name = $derived(node ? machineName(node.dns_name) || node.hostname : '');
  const host = $derived(node && !node.self ? nodeHost(node) : undefined);
  const sessionCount = $derived(
    sessions.list.filter(
      (s) => (s.host?.stable_node_id ?? sessions.self?.stable_node_id) === nodeID,
    ).length,
  );

  async function refreshNow() {
    refreshing = true;
    try {
      await loadNodes();
    } finally {
      refreshing = false;
    }
  }
</script>

<div class="mx-auto max-w-5xl p-6">
  <div class="mb-6 flex items-center gap-3">
    <a
      href="/nodes"
      class="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
      aria-label="Back to nodes"
    >
      <ArrowLeft class="size-4" />
    </a>
    <h1 class="truncate text-lg font-semibold">{name || 'Node'}</h1>
    {#if node?.self}
      <span class="shrink-0 rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground">
        this machine
      </span>
    {/if}
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

  {#if nodes === null}
    <div class="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 class="size-4 animate-spin" /> Loading…
    </div>
  {:else if !node}
    <p class="text-sm text-muted-foreground">
      That machine is not in your list any more. <a class="underline" href="/nodes">
        Back to nodes
      </a>
    </p>
  {:else}
    <div class="mb-5 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <span class="flex items-center gap-1.5">
        <span
          class="size-2 rounded-full"
          class:bg-green-500={node.online}
          class:bg-muted-foreground={!node.online}
          aria-hidden="true"
        ></span>
        {node.online ? 'online' : 'offline'}
      </span>
      <span class="font-mono">{node.dns_name}</span>
      <span>{sessionCount} {sessionCount === 1 ? 'session' : 'sessions'}</span>
    </div>

    {#if node.online}
      <NodeRuntimes {node} {host} />
    {:else}
      <p class="text-sm text-muted-foreground">
        This machine is offline, so it has nothing to report.
      </p>
    {/if}
  {/if}
</div>
