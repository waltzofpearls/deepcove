<script lang="ts">
  // Inline prompt shown on the session page when you open a session that lives on ANOTHER node while
  // in connected (direct) mode. Reaching a remote node's PTY/API goes over the in-browser Tailscale
  // node (WASM), which isn't signed in by default here, so without this the page would hang on
  // "loading…" forever. We surface the sign-in instead. Mounting also kicks the in-browser node up, so
  // the prompt both explains and drives the login; once it's running the pending calls resolve and the
  // session loads. Local sessions never render this (they stay pure-direct).
  import { onMount } from 'svelte';
  import { ensureTailnet, tailnetAuth } from '$lib/api/wasm';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import Server from '@lucide/svelte/icons/server';

  let { hostName }: { hostName?: string } = $props();
  let auth = $derived($tailnetAuth);

  function start() {
    void ensureTailnet().catch(() => {});
  }
  // Bring the in-browser node up as soon as the prompt appears, even before the page's own fetches
  // trigger it, so the login URL surfaces promptly.
  onMount(start);
</script>

<div class="absolute inset-0 flex flex-col items-center justify-center gap-4 p-6 text-center">
  {#if auth.phase === 'needs_login'}
    <Server class="size-10 text-muted-foreground" />
    <div class="max-w-sm space-y-1">
      <p class="text-sm font-medium">
        This session runs on <span class="font-mono">{hostName ?? 'another node'}</span>.
      </p>
      <p class="text-sm text-muted-foreground">
        Sign in to your tailnet to open it from here.
      </p>
    </div>
    <a
      href={auth.url}
      target="_blank"
      rel="noopener noreferrer"
      class="inline-flex h-10 items-center gap-2 rounded-md bg-primary px-5 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
    >
      Sign in with Tailscale
      <ExternalLink class="size-4" />
    </a>
    <p class="text-xs text-muted-foreground">
      A sign-in tab will open. Return here once it completes.
    </p>
  {:else if auth.phase === 'error'}
    <TriangleAlert class="size-8 text-destructive" />
    <p class="text-sm">Couldn't reach your tailnet.</p>
    {#if auth.message}
      <p class="max-w-sm break-words text-xs text-muted-foreground">{auth.message}</p>
    {/if}
    <button
      onclick={start}
      class="mt-1 inline-flex h-9 items-center rounded-md border border-border bg-background px-4 text-sm font-medium transition-colors hover:bg-secondary"
    >
      Try again
    </button>
  {:else}
    <!-- idle / connecting: the in-browser node is coming up -->
    <Loader2 class="size-8 animate-spin text-muted-foreground" />
    <p class="text-sm text-muted-foreground">Connecting to your tailnet…</p>
  {/if}
</div>
