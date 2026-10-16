<script lang="ts">
  // One node's row in the LLM proxy card. Each node keeps its own proxy and makes the calls itself, so
  // the read, the save and the test all go to that node.
  import { untrack } from 'svelte';
  import { api } from '$lib/api/client';
  import { nodeHost } from '$lib/api/sessions';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { usePolling } from '$lib/polling';
  import { Button } from '$lib/components/ui/button';
  import { machineName } from '$lib/util/tailnet';
  import type { NodeInfo } from '$lib/api/types';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import Check from '@lucide/svelte/icons/check';

  let { node }: { node: NodeInfo } = $props();

  let currentProxy = $state<string | null>(null);
  let proxyDraft = $state('');
  let loadError = $state('');
  let savingProxy = $state(false);
  let proxySaved = $state(false);
  let proxyError = $state('');
  let testingProxy = $state(false);
  let proxyTest = $state('');

  // The node list is rebuilt on every poll, so the load below keys on these values, not the object.
  const nodeKey = $derived(`${node.stable_node_id}|${node.dns_name}|${node.online}`);
  // This node rides the transport that served the page, like the Nodes page does.
  const host = $derived.by(() => {
    void nodeKey;
    return untrack(() => (node.self ? undefined : nodeHost(node)));
  });
  const nodeName = $derived(machineName(node.dns_name) || node.hostname);
  // A remote node with no address would fall back to this node, so it is shown but never asked.
  const reachable = $derived(node.self || !!node.dns_name);

  // Retried until the first read works, then left alone so a later answer never wipes out your typing.
  $effect(() => {
    void nodeKey;
    const online = untrack(() => node.online && reachable);
    const target = host;
    if (!online) return;
    const ac = new AbortController();
    const stop = usePolling(
      async () => {
        if (currentProxy !== null) return;
        try {
          const proxy = await api.llmProxy(target, ac.signal);
          currentProxy = proxy.url;
          proxyDraft = proxy.url;
          loadError = '';
        } catch (e) {
          if ((e as Error)?.name !== 'AbortError') {
            loadError = e instanceof Error ? e.message : String(e);
          }
        }
      },
      () => preferences.pollIntervalMs,
    );
    return () => {
      ac.abort();
      stop();
    };
  });

  async function testProxy() {
    testingProxy = true;
    proxyTest = '';
    proxyError = '';
    try {
      proxyTest = (await api.testLLMProxy(host)).result;
    } catch (e) {
      proxyError = e instanceof Error ? e.message : String(e);
    } finally {
      testingProxy = false;
    }
  }

  async function saveProxy() {
    savingProxy = true;
    proxyError = '';
    try {
      const proxy = await api.saveLLMProxy(proxyDraft.trim(), host);
      currentProxy = proxy.url;
      proxyDraft = proxy.url;
      proxySaved = true;
      setTimeout(() => (proxySaved = false), 1500);
    } catch (e) {
      proxyError = e instanceof Error ? e.message : String(e);
    } finally {
      savingProxy = false;
    }
  }
</script>

<div
  class="grid gap-2 border-t border-border pt-3 sm:grid-cols-[14rem_minmax(0,1fr)] sm:items-center sm:gap-4"
>
  <div class="min-w-0">
    <div class="truncate text-sm font-medium" title={nodeName}>{nodeName}</div>
    {#if node.self}<div class="text-xs text-muted-foreground">This machine</div>{/if}
  </div>
  {#if !node.online || !reachable}
    <p class="text-xs text-muted-foreground">
      {node.online ? "Can't reach this node" : 'Offline'}
    </p>
  {:else if currentProxy === null}
    <p class="text-xs" class:text-destructive={!!loadError}>{loadError || 'Loading…'}</p>
  {:else}
    <div class="flex items-center gap-2">
      <input
        bind:value={proxyDraft}
        placeholder="http://100.75.35.244:9001"
        class="h-9 min-w-0 flex-1 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
        aria-label={`LLM proxy for ${nodeName}`}
      />
      <Button
        size="sm"
        variant="ghost"
        onclick={testProxy}
        disabled={testingProxy || !currentProxy}
      >
        {#if testingProxy}
          <Loader2 class="size-4 animate-spin" />
        {/if}
        Test
      </Button>
      <Button
        size="sm"
        onclick={saveProxy}
        disabled={savingProxy || proxyDraft.trim() === currentProxy}
      >
        {#if savingProxy}
          <Loader2 class="size-4 animate-spin" />
        {:else if proxySaved}
          <Check class="size-4" />
        {/if}
        Save
      </Button>
    </div>
    {#if proxyError}
      <p class="text-xs text-destructive sm:col-start-2">{proxyError}</p>
    {/if}
    {#if proxyTest}
      <p class="text-xs text-muted-foreground sm:col-start-2">{proxyTest}</p>
    {/if}
  {/if}
</div>
