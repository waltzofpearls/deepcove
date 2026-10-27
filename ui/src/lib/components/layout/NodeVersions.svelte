<script lang="ts">
  // The version in the sidebar footer. A click opens every node's version, so a node left behind stands
  // out.
  import { api } from '$lib/api/client';
  import { cn } from '$lib/utils';
  import { machineName } from '$lib/util/tailnet';
  import { isBehind } from '$lib/util/version';
  import type { NodeInfo } from '$lib/api/types';
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
  } from '$lib/components/ui/dialog';
  import { Badge } from '$lib/components/ui/badge';
  import LogoIcon from './LogoIcon.svelte';

  const uiVersion = __DEEPCOVE_VERSION__;
  let open = $state(false);
  let nodes = $state<NodeInfo[]>([]);
  let loadFailed = $state(false);
  const anyBehind = $derived(
    nodes.some((node) => node.version && isBehind(node.version, uiVersion)),
  );

  // Asked each time it opens, so a node upgraded since then shows its new version.
  $effect(() => {
    if (!open) return;
    const ac = new AbortController();
    api
      .nodes(ac.signal)
      .then((listed) => {
        nodes = listed;
        loadFailed = false;
      })
      .catch(() => {
        if (!ac.signal.aborted) loadFailed = true;
      });
    return () => ac.abort();
  });
</script>

<button
  type="button"
  onclick={() => (open = true)}
  class="flex items-center gap-1.5 p-3 text-xs text-muted-foreground transition-colors hover:text-foreground"
>
  <LogoIcon class="size-3.5" />
  <span class="font-mono">deepcove {uiVersion}</span>
</button>

<Dialog bind:open>
  <DialogContent class="gap-5 p-6 sm:max-w-2xl">
    <DialogHeader>
      <DialogTitle>Versions</DialogTitle>
      <DialogDescription>
        This app is deepcove {uiVersion}.
      </DialogDescription>
    </DialogHeader>

    {#if loadFailed}
      <p class="text-sm text-muted-foreground">Could not reach your nodes.</p>
    {:else}
      <table class="w-full text-sm">
        <thead class="text-left text-xs text-muted-foreground">
          <tr>
            <th class="pb-2 font-medium">Node</th>
            <th class="pb-2 font-medium">Status</th>
            <th class="pb-2 text-right font-medium">Version</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-border">
          {#each nodes as node (node.stable_node_id || node.hostname)}
            <tr>
              <td class="py-2 pr-4 font-mono break-all">
                {machineName(node.dns_name) || node.hostname}
              </td>
              <td class="py-2 pr-4 whitespace-nowrap">
                <span class="flex items-center gap-2 text-muted-foreground">
                  <span
                    class={cn(
                      'size-1.5 rounded-full',
                      node.online ? 'bg-emerald-500' : 'bg-muted-foreground',
                    )}
                  ></span>
                  {node.online ? 'online' : 'offline'}
                </span>
              </td>
              <td class="py-2 text-right whitespace-nowrap">
                <span class="font-mono">{node.version || 'unknown'}</span>
                {#if node.version && isBehind(node.version, uiVersion)}
                  <Badge
                    variant="outline"
                    class="ml-2 border-amber-500/50 text-amber-600 dark:text-amber-400"
                  >
                    behind
                  </Badge>
                {/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if anyBehind}
        <p class="text-xs text-muted-foreground">
          Behind? Run <code class="font-mono">deepcove upgrade</code>
          on that node.
        </p>
      {/if}
    {/if}
  </DialogContent>
</Dialog>
