<script lang="ts">
  import { api } from '$lib/api/client';
  import { usePolling } from '$lib/polling';
  import { discoveryRequired } from '$lib/api/transport';
  import { tailnetAuth, webNodeInfo, wasmHostname, type WebNodeInfo } from '$lib/api/wasm';
  import { machineName } from '$lib/util/tailnet';
  import { discovery } from '$lib/stores/discovery.svelte';
  import { preferences } from '$lib/stores/preferences.svelte';
  import TailnetConnect from '$lib/components/TailnetConnect.svelte';
  import { Button } from '$lib/components/ui/button';
  import type { ConnectStatus, NodeInfo } from '$lib/api/types';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Server from '@lucide/svelte/icons/server';
  import Globe from '@lucide/svelte/icons/globe';
  import Network from '@lucide/svelte/icons/network';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';

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
    if (discoveryRequired()) return; // discover mode auto-updates via the discovery store (netmap poll)
    const ac = new AbortController();
    const stop = usePolling(
      () => loadDirect(ac.signal),
      () => preferences.pollIntervalMs,
    );
    return () => {
      ac.abort();
      stop();
    };
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

  let refreshing = $state(false);
  async function refreshNow() {
    refreshing = true;
    try {
      if (discoveryRequired()) discovery.refreshNow();
      else await loadDirect();
    } catch {
      /* transient; the next poll retries */
    } finally {
      refreshing = false;
    }
  }
</script>

<div class="mx-auto max-w-5xl p-6">
  <div class="mb-6 flex items-center gap-3">
    <a
      href="/"
      class="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
      aria-label="Back to dashboard"
    >
      <ArrowLeft class="size-4" />
    </a>
    <h1 class="text-lg font-semibold">Nodes</h1>
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
            deepcove is running locally. Connect to your Tailscale network to see your other
            machines here.
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
    <!-- One row for every node. The chip says which kind it is, so machines and browsers read as one
         list rather than two. -->
    {#snippet nodeRow(opts: {
      kind: 'session' | 'web';
      name: string;
      address?: string;
      mine?: string;
      online: boolean;
    })}
      <li class="flex items-center gap-3 rounded-lg border border-border bg-card p-3">
        {#if opts.kind === 'session'}
          <Server class="size-5 shrink-0 text-muted-foreground" />
        {:else}
          <Globe class="size-5 shrink-0 text-muted-foreground" />
        {/if}
        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2">
            <span class="truncate font-medium">{opts.name}</span>
            <!-- Two node kinds, two colours. Ownership below stays neutral grey, so a category and an
                 annotation never read as the same kind of fact. -->
            {#if opts.kind === 'session'}
              <span
                class="shrink-0 rounded bg-blue-500/10 px-1.5 py-0.5 text-xs text-blue-700 ring-1 ring-inset ring-blue-600/25 dark:bg-blue-500/15 dark:text-blue-300 dark:ring-blue-500/30"
              >
                Session node
              </span>
            {:else}
              <span
                class="shrink-0 rounded bg-violet-500/10 px-1.5 py-0.5 text-xs text-violet-700 ring-1 ring-inset ring-violet-600/25 dark:bg-violet-500/15 dark:text-violet-300 dark:ring-violet-500/30"
              >
                Web node
              </span>
            {/if}
            {#if opts.mine}
              <span
                class="shrink-0 rounded bg-secondary px-1.5 py-0.5 text-xs text-muted-foreground"
              >
                {opts.mine}
              </span>
            {/if}
          </div>
          {#if opts.address}
            <div class="truncate font-mono text-xs text-muted-foreground">{opts.address}</div>
          {/if}
        </div>
        <div class="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
          <span
            class="size-2 rounded-full"
            class:bg-green-500={opts.online}
            class:bg-muted-foreground={!opts.online}
            aria-hidden="true"
          ></span>
          {opts.online ? 'online' : 'offline'}
        </div>
      </li>
    {/snippet}

    <section>
      <p class="mb-3 text-xs text-muted-foreground">
        Your machines running deepcove, and the browsers signed in to your tailnet. Sessions live on
        the machine that created them.
      </p>
      <ul class="space-y-2">
        {#each nodes as node (node.stable_node_id || node.hostname)}
          {@render nodeRow({
            kind: 'session',
            name: machineName(node.dns_name) || node.hostname,
            address: node.dns_name,
            mine: node.self ? 'this machine' : undefined,
            online: node.online,
          })}
        {/each}
        {#if webRunning}
          {@render nodeRow({
            kind: 'web',
            name: webInfo?.name ?? wasmHostname(),
            address: webInfo?.ipv4,
            mine: 'this browser',
            online: true,
          })}
        {/if}
        {#each otherWebNodes as node (node.stable_node_id || node.dns_name)}
          {@render nodeRow({
            kind: 'web',
            name: machineName(node.dns_name),
            address: node.dns_name,
            online: true,
          })}
        {/each}
      </ul>
    </section>
  {/if}
</div>
