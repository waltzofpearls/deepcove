<script lang="ts">
  import { untrack } from 'svelte';
  import { cn } from '$lib/utils';
  import { api, preflightTimeoutMs } from '$lib/api/client';
  import { nodeHost } from '$lib/api/sessions';
  import { runtimeSummary } from '$lib/util/runtime';
  import type { NodeInfo, RuntimesInfo } from '$lib/api/types';
  import Server from '@lucide/svelte/icons/server';

  let {
    nodes,
    picked,
    defaultId,
    onpick,
  }: {
    nodes: NodeInfo[];
    picked: string;
    defaultId: string;
    onpick: (stableNodeId: string) => void;
  } = $props();

  // What each node can run, asked of the node itself. One that does not answer says so on its card
  // rather than holding up the others.
  let runtimesByNode = $state<Record<string, RuntimesInfo>>({});
  let silentNodes = $state<Record<string, boolean>>({});
  const nodeIds = $derived(nodes.map((node) => node.stable_node_id).join(','));
  $effect(() => {
    void nodeIds;
    const list = untrack(() => nodes);
    const asking: AbortController[] = [];
    for (const node of list) {
      const controller = new AbortController();
      asking.push(controller);
      api
        .preflight(node.self ? undefined : nodeHost(node), controller.signal, preflightTimeoutMs)
        .then((preflight) => {
          runtimesByNode = { ...runtimesByNode, [node.stable_node_id]: preflight.runtimes };
        })
        .catch(() => {
          if (!controller.signal.aborted) {
            silentNodes = { ...silentNodes, [node.stable_node_id]: true };
          }
        });
    }
    return () => {
      for (const controller of asking) controller.abort();
    };
  });

  function canRunLine(node: NodeInfo): string {
    const info = runtimesByNode[node.stable_node_id];
    if (info) return runtimeSummary(info);
    if (silentNodes[node.stable_node_id]) return "Didn't answer. You can still pick it.";
    return 'Checking what it can run…';
  }

  const groupName = $props.id();
</script>

<div class="space-y-4">
  <div class="space-y-1">
    <h2 class="text-lg font-semibold">Which node?</h2>
    <p class="text-sm text-muted-foreground">The session runs on the node you pick.</p>
  </div>

  <div class="grid gap-2" role="radiogroup" aria-label="Node">
    {#each nodes as node (node.stable_node_id)}
      {@const selected = node.stable_node_id === picked}
      <label
        class={cn(
          'flex cursor-pointer gap-3 rounded-lg border p-3.5 text-left transition-colors hover:bg-accent/50 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring',
          selected && 'border-primary bg-primary/10',
        )}
      >
        <input
          type="radio"
          class="sr-only"
          name={groupName}
          value={node.stable_node_id}
          checked={selected}
          onchange={() => onpick(node.stable_node_id)}
        />
        <span
          class={cn(
            'flex size-9 shrink-0 items-center justify-center rounded-md',
            selected ? 'bg-primary/15 text-primary' : 'bg-secondary text-muted-foreground',
          )}
        >
          <Server class="size-5" />
        </span>
        <span class="flex min-w-0 flex-col gap-1">
          <span class="flex flex-wrap items-center gap-2">
            <span class="truncate font-mono text-sm font-semibold">{node.hostname}</span>
            {#if node.self}
              <span class="text-xs text-muted-foreground">This node</span>
            {/if}
            {#if node.stable_node_id === defaultId}
              <span
                class="rounded-full border border-primary/50 px-1.5 text-[11px] font-semibold text-primary"
              >
                Default
              </span>
            {/if}
          </span>
          <span class="text-[13px] leading-snug text-muted-foreground">{canRunLine(node)}</span>
        </span>
      </label>
    {/each}
  </div>
</div>
