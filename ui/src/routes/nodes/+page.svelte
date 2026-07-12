<script lang="ts">
  import { api } from '$lib/api/client';
  import { usePolling } from '$lib/polling';
  import { discoveryRequired } from '$lib/api/transport';
  import { tailnetAuth, webNodeInfo, wasmHostname, type WebNodeInfo } from '$lib/api/wasm';
  import { machineName } from '$lib/util/tailnet';
  import { nodeClusterLabel } from '$lib/util/cluster';
  import { discovery } from '$lib/stores/discovery.svelte';
  import TailnetConnect from '$lib/components/TailnetConnect.svelte';
  import ClusterBadge from '$lib/components/ClusterBadge.svelte';
  import type { ConnectStatus, NodeInfo } from '$lib/api/types';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Server from '@lucide/svelte/icons/server';
  import Globe from '@lucide/svelte/icons/globe';
  import Layers from '@lucide/svelte/icons/layers';
  import Network from '@lucide/svelte/icons/network';
  import Loader2 from '@lucide/svelte/icons/loader-2';

  // Your deepcove machines: this one plus same-user tailnet peers. Connected mode reads them from a
  // server (GET /api/nodes); dashboard mode reads the in-browser node's own discovery (status()).
  let fetched = $state<NodeInfo[] | null>(null);
  let fetchedWeb = $state<NodeInfo[]>([]);
  let connect = $state<ConnectStatus | null>(null);
  let error = $state<string | null>(null);

  // In direct (connected/local) mode, a node only has peers once it's joined the tailnet. So we first
  // check connect status: not running → it's a lone rung-1 node, and we show the connect CTA instead of
  // a lonely "this machine" row. Dashboard (discover) mode rides the in-browser node, which is its own
  // tailnet connection, so it skips this and always lists discovered nodes.
  async function loadDirect(signal?: AbortSignal) {
    try {
      const cs = await api.connectStatus(signal);
      connect = cs;
      if (cs.phase === 'running') {
        fetched = await api.nodes(signal);
        fetchedWeb = await api.webNodes(signal);
      }
      error = null;
    } catch (e) {
      if (!signal?.aborted) error = e instanceof Error ? e.message : String(e);
    }
  }

  $effect(() => {
    if (discoveryRequired()) return; // discover mode reads the discovery store reactively below
    const ac = new AbortController();
    void loadDirect(ac.signal);
    return () => ac.abort();
  });

  const connected = $derived(discoveryRequired() || connect?.phase === 'running');
  const nodes = $derived(discoveryRequired() ? discovery.nodes : fetched);

  // Web node = this browser's own in-browser Tailscale device. Shown as a distinct kind from the
  // session nodes (your machines). Live details load once it's running (signed in).
  const webRunning = $derived($tailnetAuth.phase === 'running');
  let webInfo = $state<WebNodeInfo | null>(null);
  $effect(() => {
    if (webRunning) {
      webNodeInfo()
        .then((i) => (webInfo = i))
        .catch(() => {});
    } else {
      webInfo = null;
    }
  });

  // Other browsers' web nodes: online in-browser peers from the server (connected mode) or the
  // in-browser netmap (dashboard mode), minus this browser, which we show from its own richer webInfo.
  const webPeers = $derived(discoveryRequired() ? discovery.webNodes : fetchedWeb);
  const otherWebNodes = $derived(webPeers.filter((n) => n.dns_name !== webInfo?.dnsName));

  // Cluster tag per session node (SPEC §5.12), via /api/nodes/clusters (covers connected + dashboard mode).
  let clusterMap = $state<Record<string, string>>({});
  let clustersLoaded = $state(false);
  const CLUSTER_REFRESH_MS = 10_000; // renames are rare; no need for the fast sessions poll rate
  const nodeIdsKey = $derived(
    (nodes ?? [])
      .map((n) => n.stable_node_id)
      .sort()
      .join(',')
  );
  $effect(() => {
    // Key on nodeIdsKey (a stable string), not the `nodes` array, whose reference churns every netmap tick.
    if (!nodeIdsKey) return;
    // Poll (not on the netmap tick) so a peer's cluster rename self-heals without a reload.
    return usePolling(
      async () => {
        try {
          const m = await api.nodeClusters();
          // merge, not replace, so a momentarily-unreachable node keeps its last-known cluster
          clusterMap = { ...clusterMap, ...m };
          clustersLoaded = true;
        } catch {
          /* keep last-known; the next tick retries */
        }
      },
      () => CLUSTER_REFRESH_MS
    );
  });
  const clusterFor = (n: NodeInfo): string => nodeClusterLabel(clusterMap, n.stable_node_id);
  // Gate on clustersLoaded so nodes don't flash unknown/grouping before the first fetch.
  const nodeClustersInView = $derived(clustersLoaded ? [...new Set((nodes ?? []).map(clusterFor))] : []);
  const showNodeClusters = $derived(nodeClustersInView.length > 1);
  const groupedNodes = $derived.by(() => {
    const m = new Map<string, NodeInfo[]>();
    for (const n of nodes ?? []) {
      const c = clusterFor(n);
      const arr = m.get(c);
      if (arr) arr.push(n);
      else m.set(c, [n]);
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b));
  });
</script>

<div class="mx-auto max-w-2xl p-6">
  <div class="mb-6 flex items-center gap-3">
    <a
      href="/"
      class="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
      aria-label="Back to dashboard"
    >
      <ArrowLeft class="size-4" />
    </a>
    <h1 class="text-lg font-semibold">Nodes</h1>
  </div>

  {#if error}
    <div class="text-sm text-destructive">{error}</div>
  {:else if !connected && connect === null}
    <!-- Still checking whether this node is on a tailnet. -->
    <div class="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 class="size-4 animate-spin" /> Loading…
    </div>
  {:else if !connected}
    <!-- Rung 1: a lone local node. Surface the connect flow right here, where you'd look for your
         other machines, instead of leaving it buried in Settings. -->
    <div class="rounded-xl border border-border bg-card p-6">
      <div class="mb-4 flex items-start gap-3">
        <div
          class="flex size-10 shrink-0 items-center justify-center rounded-lg bg-secondary text-muted-foreground"
        >
          <Network class="size-5" />
        </div>
        <div class="space-y-1">
          <h2 class="text-sm font-semibold">It's just this machine right now</h2>
          <p class="text-sm text-muted-foreground">
            deepcove is running locally. Connect to your tailnet to bring your other machines into one
            cluster and see them all here.
          </p>
        </div>
      </div>
      <TailnetConnect onconnected={() => void loadDirect()} />
    </div>
  {:else if nodes === null}
    <div class="flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 class="size-4 animate-spin" /> Loading…
    </div>
  {:else}
    <!-- One node row, reused by both the flat list and the per-cluster groups below. -->
    {#snippet nodeRow(node: NodeInfo)}
      <li class="flex items-center gap-3 rounded-lg border border-border bg-card p-3">
        <Server class="size-5 shrink-0 text-muted-foreground" />
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="truncate font-medium">{machineName(node.dns_name) || node.hostname}</span>
            {#if node.self}
              <span class="rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground"
                >this machine</span
              >
            {/if}
            <ClusterBadge cluster={clustersLoaded ? clusterFor(node) : ''} />
          </div>
          {#if node.dns_name}
            <div class="truncate font-mono text-xs text-muted-foreground">{node.dns_name}</div>
          {/if}
        </div>
        <div class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
          <span
            class="size-2 rounded-full"
            class:bg-green-500={node.online}
            class:bg-muted-foreground={!node.online}
            aria-hidden="true"
          ></span>
          {node.online ? 'online' : 'offline'}
        </div>
      </li>
    {/snippet}

    <!-- Session nodes: your machines running deepcove (they host the containers/sessions). Grouped by
         cluster once there's more than one; a flat list otherwise. -->
    <section class="mb-6">
      <h2 class="mb-1 text-sm font-semibold">Session nodes</h2>
      <p class="mb-3 text-xs text-muted-foreground">
        Your machines running deepcove. Sessions live on the machine that created them.
      </p>
      {#if showNodeClusters}
        {#each groupedNodes as [cluster, group], i (cluster)}
          <div class="mb-1.5 flex items-center gap-2" class:mt-4={i > 0}>
            <Layers class="size-3.5 shrink-0 text-muted-foreground" />
            <h3 class="text-xs font-semibold text-muted-foreground">{cluster}</h3>
            <span class="text-xs text-muted-foreground">{group.length}</span>
            <div class="ml-1 h-px flex-1 bg-border"></div>
          </div>
          <ul class="space-y-2">
            {#each group as node (node.stable_node_id || node.hostname)}
              {@render nodeRow(node)}
            {/each}
          </ul>
        {/each}
      {:else}
        <ul class="space-y-2">
          {#each nodes as node (node.stable_node_id || node.hostname)}
            {@render nodeRow(node)}
          {/each}
        </ul>
      {/if}
    </section>

    <!-- Web nodes: your browser as its own tailnet device, plus any other browsers you've signed in.
         Online only (they're ephemeral). This browser is marked and shown from its own richer info;
         the rest come from the tailnet (server in connected mode, in-browser netmap in dashboard). -->
    <section>
      <h2 class="mb-1 text-sm font-semibold">Web nodes</h2>
      <p class="mb-3 text-xs text-muted-foreground">
        Your browser, as its own device on your tailnet. This is how it reaches sessions on other
        machines.
      </p>
      {#if webRunning || otherWebNodes.length > 0}
        <ul class="space-y-2">
          {#if webRunning}
            <li class="flex items-center gap-3 rounded-lg border border-border bg-card p-3">
              <Globe class="size-5 shrink-0 text-muted-foreground" />
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2">
                  <span class="truncate font-medium">{webInfo?.name ?? wasmHostname()}</span>
                  <span class="rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground"
                    >this browser</span
                  >
                </div>
                {#if webInfo?.ipv4}
                  <div class="truncate font-mono text-xs text-muted-foreground">{webInfo.ipv4}</div>
                {/if}
              </div>
              <div class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
                <span class="size-2 rounded-full bg-green-500" aria-hidden="true"></span>
                online
              </div>
            </li>
          {/if}
          {#each otherWebNodes as node (node.stable_node_id || node.dns_name)}
            <li class="flex items-center gap-3 rounded-lg border border-border bg-card p-3">
              <Globe class="size-5 shrink-0 text-muted-foreground" />
              <div class="min-w-0 flex-1">
                <span class="truncate font-medium">{machineName(node.dns_name)}</span>
                {#if node.dns_name}
                  <div class="truncate font-mono text-xs text-muted-foreground">{node.dns_name}</div>
                {/if}
              </div>
              <div class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
                <span class="size-2 rounded-full bg-green-500" aria-hidden="true"></span>
                online
              </div>
            </li>
          {/each}
        </ul>
        {#if !webRunning}
          <p class="mt-2 text-xs text-muted-foreground">
            This browser isn't on your tailnet yet. Use the <span class="font-medium">Web node</span>
            chip in the top bar to sign in.
          </p>
        {/if}
      {:else}
        <p class="rounded-lg border border-dashed border-border p-3 text-xs text-muted-foreground">
          This browser isn't on your tailnet yet. Use the <span class="font-medium">Web node</span> chip
          in the top bar to sign in.
        </p>
      {/if}
    </section>
  {/if}
</div>
