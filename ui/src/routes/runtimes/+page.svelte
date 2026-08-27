<script lang="ts">
  import { api } from '$lib/api/client';
  import { discovery } from '$lib/stores/discovery.svelte';
  import { discoveryRequired } from '$lib/api/transport';
  import { nodeHost } from '$lib/api/sessions';
  import { machineName } from '$lib/util/tailnet';
  import NodeRuntimes from '$lib/components/runtimes/NodeRuntimes.svelte';
  import { Button } from '$lib/components/ui/button';
  import type { NodeInfo } from '$lib/api/types';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';

  // Which machines to show. Same source as the nodes page: discovery in dashboard mode, this node's
  // own list otherwise.
  let nodes = $state<NodeInfo[] | null>(null);
  let refreshing = $state(false);

  // This machine first; the rest by name, so the list does not reshuffle as nodes come and go.
  function ordered(list: NodeInfo[]): NodeInfo[] {
    return [...list].sort((a, b) => {
      if (a.self !== b.self) return a.self ? -1 : 1;
      return (a.hostname ?? '').localeCompare(b.hostname ?? '');
    });
  }

  async function loadNodes() {
    try {
      nodes = ordered(discoveryRequired() ? discovery.nodes : await api.nodes());
    } catch {
      nodes = [];
    }
  }

  $effect(() => {
    void loadNodes();
  });

  // Each section reloads itself, so this only refreshes which machines exist.
  async function refreshNow() {
    refreshing = true;
    try {
      if (discoveryRequired()) discovery.refreshNow();
      await loadNodes();
    } finally {
      refreshing = false;
    }
  }
</script>

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
    What each of your machines is hosting, straight from its runtimes. Virtual machines run inside
    deepcove itself, so this is the only place they show up.
  </p>

  {#if nodes === null}
    <div class="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 class="size-4 animate-spin" /> Loading…
    </div>
  {:else}
    {#each nodes as node (node.stable_node_id || node.hostname)}
      <section class="mb-8">
        <h2 class="mb-2 flex items-center gap-2 text-sm font-semibold">
          {machineName(node.dns_name) || node.hostname}
          {#if node.self}
            <span
              class="rounded bg-secondary px-1.5 py-0.5 text-xs font-normal text-muted-foreground"
            >
              this machine
            </span>
          {/if}
        </h2>
        {#if node.online}
          <NodeRuntimes {node} host={node.self ? undefined : nodeHost(node)} />
        {:else}
          <p class="text-sm text-muted-foreground">
            This machine is offline, so it has nothing to report.
          </p>
        {/if}
      </section>
    {/each}
  {/if}
</div>
