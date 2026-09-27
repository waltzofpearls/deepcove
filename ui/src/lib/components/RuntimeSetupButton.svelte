<script lang="ts">
  import type { SessionHost } from '$lib/api/types';
  // Downloads whatever a runtime is missing, then tells the page to refresh. The runtime is usable as
  // soon as this finishes, so there is deliberately no "restart deepcove" instruction anywhere here.
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import { Button } from '$lib/components/ui/button';
  import Download from '@lucide/svelte/icons/download';
  import Loader2 from '@lucide/svelte/icons/loader-2';

  // autostart begins the setup as soon as this appears, for a page whose whole job is that setup. busy
  // is bindable so such a page can hold its own buttons while the download runs.
  let {
    runtime,
    host,
    ondone,
    autostart = false,
    busy = $bindable(false),
  }: {
    runtime: string;
    host?: SessionHost;
    // Awaited, so the button stays busy until the page has caught up with the new runtime.
    ondone?: () => void | Promise<unknown>;
    autostart?: boolean;
    busy?: boolean;
  } = $props();

  onMount(() => {
    if (autostart) void setUp();
  });
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
      await ondone?.();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }
</script>

<!-- Always on the page, so a screen reader hears the first status, and only words, never each percent. -->
<div role="status" aria-live="polite" class="sr-only">{busy ? status || 'Setting up…' : ''}</div>

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
    {error ? 'Try again' : 'Set up'}
  </Button>
{/if}

{#if error}
  <p role="alert" class="mt-1.5 text-xs text-destructive">{error}</p>
{/if}
