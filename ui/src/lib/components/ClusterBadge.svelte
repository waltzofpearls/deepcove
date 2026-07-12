<script lang="ts">
  import Layers from '@lucide/svelte/icons/layers';
  import CircleDashed from '@lucide/svelte/icons/circle-dashed';
  import { clusterBadgeKind } from '$lib/util/cluster';

  // Cluster tag (card, zoom, Nodes page); kinds classified by clusterBadgeKind.
  let { cluster }: { cluster: string } = $props();
  const kind = $derived(clusterBadgeKind(cluster));
</script>

{#if kind === 'unknown'}
  <span
    class="flex shrink-0 items-center gap-1 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground ring-1 ring-inset ring-border"
  >
    <CircleDashed class="size-3 shrink-0" />
    unknown
  </span>
{:else if kind === 'named'}
  <span
    class="flex min-w-0 items-center gap-1 rounded bg-violet-500/10 px-1.5 py-0.5 text-xs text-violet-700 ring-1 ring-inset ring-violet-600/25 dark:bg-violet-500/15 dark:text-violet-300 dark:ring-violet-500/30"
  >
    <Layers class="size-3 shrink-0" />
    <span class="truncate">{cluster}</span>
  </span>
{/if}
