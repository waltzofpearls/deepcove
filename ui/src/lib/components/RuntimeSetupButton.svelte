<script lang="ts">
  import type { SessionHost } from '$lib/api/types';
  // Downloads whatever a runtime is missing, then tells the page to refresh. The runtime is usable as
  // soon as this finishes, so there is deliberately no "restart deepcove" instruction anywhere here.
  import { api } from '$lib/api/client';
  import { Button } from '$lib/components/ui/button';
  import Download from '@lucide/svelte/icons/download';
  import Loader2 from '@lucide/svelte/icons/loader-2';

  let { runtime, host, ondone }: { runtime: string; host?: SessionHost; ondone?: () => void } =
    $props();

  let busy = $state(false);
  // -1 until the download reports a total, which is what the indeterminate case looks like.
  let percent = $state(-1);
  let status = $state('');
  let error = $state<string | null>(null);

  async function setUp() {
    busy = true;
    error = null;
    percent = -1;
    try {
      await api.setupRuntime(
        runtime,
        (p) => {
          status = p.status;
          percent = p.percent;
        },
        host,
      );
      ondone?.();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

{#if busy}
  <div class="flex items-center gap-2 text-xs text-muted-foreground">
    <Loader2 class="size-3.5 shrink-0 animate-spin" />
    <span>{status || 'Setting up…'}</span>
    {#if percent >= 0}
      <div class="h-1.5 w-24 shrink-0 overflow-hidden rounded-full bg-secondary">
        <div
          class="h-full rounded-full bg-primary transition-[width] duration-200"
          style="width: {percent}%"
        ></div>
      </div>
      <span class="tabular-nums">{percent}%</span>
    {/if}
  </div>
{:else}
  <Button size="sm" onclick={setUp}>
    <Download class="size-4" />
    Set up
  </Button>
{/if}

{#if error}
  <p class="mt-1.5 text-xs text-destructive">{error}</p>
{/if}
