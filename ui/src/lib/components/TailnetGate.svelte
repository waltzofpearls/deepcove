<script lang="ts">
  // Full-screen gate shown in wasm mode until the in-browser Tailscale node is
  // running. The dashboard is never mounted until then, so an expired/invalid
  // session lands on the login view without flashing the app. The layout only
  // renders this while `transport.mode === 'wasm' && phase !== 'running'`.
  import { ensureTailnet, tailnetAuth } from '$lib/api/wasm';
  import { discoveryRequired } from '$lib/api/transport';
  import { discovery } from '$lib/stores/discovery.svelte';
  import Logo from './layout/Logo.svelte';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import Server from '@lucide/svelte/icons/server';

  let auth = $derived($tailnetAuth);
  // Once signed in, dashboard discover mode has a second gated step: finding the cluster on the
  // tailnet. We surface that here so the pre-app screen explains what it's waiting on.
  let discovering = $derived(discoveryRequired() && auth.phase === 'running');

  function retry() {
    void ensureTailnet().catch(() => {});
  }
</script>

<div
  class="flex h-screen flex-col items-center justify-center gap-6 bg-background px-6 text-foreground"
>
  <!-- Brand anchor for the whole gate, shown in every state (login,
       error, connecting) so the pre-dashboard screen reads as deepcove. -->
  <Logo class="h-8 w-auto" />

  {#if auth.phase === 'needs_login'}
    <div class="flex w-full max-w-sm flex-col items-center gap-4 text-center">
      <p class="text-sm text-muted-foreground">
        This deepcove runs on your tailnet. Sign in to continue.
      </p>
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
    </div>
  {:else if auth.phase === 'error'}
    <div class="flex w-full max-w-sm flex-col items-center gap-3 text-center">
      <TriangleAlert class="size-8 text-destructive" />
      <p class="text-sm">Couldn't connect to your tailnet.</p>
      <p class="break-words text-xs text-muted-foreground">{auth.message}</p>
      <button
        onclick={retry}
        class="mt-1 inline-flex h-9 items-center rounded-md border border-border bg-background px-4 text-sm font-medium transition-colors hover:bg-secondary"
      >
        Try again
      </button>
    </div>
  {:else if discovering}
    <!-- Signed in; now finding the cluster on the tailnet (dashboard discover mode). -->
    {#if discovery.phase === 'error'}
      <div class="flex w-full max-w-sm flex-col items-center gap-3 text-center">
        <TriangleAlert class="size-8 text-destructive" />
        <p class="text-sm">Couldn't discover your deepcove nodes.</p>
        {#if discovery.error}
          <p class="break-words text-xs text-muted-foreground">{discovery.error}</p>
        {/if}
      </div>
    {:else if discovery.phase === 'none'}
      <div class="flex w-full max-w-sm flex-col items-center gap-3 text-center">
        <Server class="size-8 text-muted-foreground" />
        <p class="text-sm">No deepcove nodes found on your tailnet.</p>
        <p class="text-xs text-muted-foreground">
          Start deepcove on a machine that's on this tailnet, then this page will pick it up
          automatically.
        </p>
        <Loader2 class="mt-1 size-4 animate-spin text-muted-foreground" />
      </div>
    {:else}
      <Loader2 class="size-8 animate-spin text-muted-foreground" />
      <p class="text-sm text-muted-foreground">Discovering your cluster…</p>
    {/if}
  {:else}
    <!-- idle / connecting: verifying a returning session or first boot -->
    <Loader2 class="size-8 animate-spin text-muted-foreground" />
    <p class="text-sm text-muted-foreground">Connecting to your tailnet…</p>
  {/if}
</div>
