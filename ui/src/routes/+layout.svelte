<script lang="ts">
  import '../app.css';

  import { onMount, setContext } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { opensInNewTab } from '$lib/util/links';
  import { ModeWatcher } from 'mode-watcher';
  import TopBar from '$lib/components/layout/TopBar.svelte';
  import Sidebar from '$lib/components/layout/Sidebar.svelte';
  import NewSessionDialog from '$lib/components/session/NewSessionDialog.svelte';
  import TailnetGate from '$lib/components/TailnetGate.svelte';
  import { transport, discoveryRequired } from '$lib/api/transport';
  import { ensureTailnet, tailnetAuth, hasWebNodeSignIn } from '$lib/api/wasm';
  import { discovery } from '$lib/stores/discovery.svelte';
  import { Sheet, SheetContent } from '$lib/components/ui/sheet';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { missingReasons, runtimeSetupOffered } from '$lib/util/runtime';
  import { NEW_SESSION_CTX } from '$lib/context';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';
  import Loader2 from '@lucide/svelte/icons/loader-2';

  let { children } = $props();
  let newSessionOpen = $state(false);
  // Mobile-only: tracks the slide-in sidebar drawer. Desktop renders
  // the sidebar as a permanent left aside and ignores this.
  let sidebarOpen = $state(false);

  // Let any page (e.g. the dashboard's empty-state CTA) or the sidebar
  // open the New Session dialog, which lives here at the layout level.
  // Also dismiss the mobile sidebar drawer so the dialog isn't stacked
  // behind it when triggered from the sidebar's "New session" button.
  setContext(NEW_SESSION_CTX, () => {
    newSessionOpen = true;
    sidebarOpen = false;
  });

  // In wasm mode the whole app rides the in-browser Tailscale node, so bring it
  // up on boot and gate the dashboard until it's running. Direct mode never
  // touches this. `tailnetAuth` stays idle and `gated` is false. In dashboard
  // discover mode there's also no server to fan out, so the in-browser node
  // discovers your nodes itself (discovery.start brings the node up too).
  onMount(() => {
    if (transport.mode === 'wasm') {
      if (discoveryRequired()) void discovery.start();
      else void ensureTailnet().catch(() => {});
      return;
    }
    // Direct mode: the web node is lazy, but if it was signed in before (saved creds present), reconnect
    // it in the background so its chip persists across refreshes. The localStorage check is cheap and
    // synchronous, and the reconnect is fire-and-forget, so page load is never blocked; the wasm loads
    // and reconnects async and the UI updates reactively.
    if (hasWebNodeSignIn()) void ensureTailnet().catch(() => {});
  });
  // Gate until the node is running, and in discover mode until your nodes have been discovered (a
  // primary picked), because otherwise the boot transport has no target and the app would fire calls
  // into the void.
  let gated = $derived(
    transport.mode === 'wasm' &&
      ($tailnetAuth.phase !== 'running' || (discoveryRequired() && discovery.phase !== 'ready')),
  );

  // Environment-readiness poller; powers the nothing-can-run banner below
  // and the New Session dialog's gating. Lazy and ref-counted. In discover mode it
  // rides the boot transport's primary node, so hold it until discovery is ready.
  $effect(() => {
    if (discoveryRequired() && discovery.phase !== 'ready') return;
    return preflight.subscribe();
  });

  let retrying = $state(false);
  async function retryPreflight() {
    retrying = true;
    await preflight.refresh();
    retrying = false;
  }

  // Close the mobile drawer on every route change so tapping a nav
  // link inside the drawer auto-dismisses it. Cheap idempotent
  // operation when the drawer is already closed.
  $effect(() => {
    void page.url;
    sidebarOpen = false;
  });
</script>

<!-- ModeWatcher applies the .dark class on <html> based on system pref
     + user override stored in localStorage. Defaults to system. -->
<ModeWatcher defaultMode="system" />

{#if gated}
  <!-- Wasm mode: hold the whole app behind the connect/login gate until the
       in-browser node is running. Nothing mounts before then, the setup wizard included, since
       it asks a node what it can run. -->
  <TailnetGate />
{:else if page.url.pathname === '/setup'}
  <!-- The setup wizard is its own full-screen page with no topbar/sidebar chrome. -->
  {@render children()}
{:else}
  <div class="flex h-screen flex-col bg-background text-foreground">
    <TopBar onNewSession={() => (newSessionOpen = true)} onMenu={() => (sidebarOpen = true)} />

    {#if !preflight.ready}
      <div
        class="flex items-center gap-3 border-b border-destructive/40 bg-destructive/10 px-4 py-2 text-sm text-foreground"
        role="alert"
      >
        <CircleAlert class="size-4 shrink-0 text-destructive" />
        <span class="min-w-0 flex-1">
          Nothing on {transport.mode === 'wasm' ? 'your node' : 'this machine'} can host a session
          {runtimeSetupOffered(preflight.runtimes) ? 'yet' : 'right now'}, so new ones can't be
          created.
          <!-- With nothing to set up, the reason is the only thing that helps. -->
          {#if !runtimeSetupOffered(preflight.runtimes) && preflight.runtimes}
            {missingReasons(preflight.runtimes).join(' ')}
          {/if}
        </span>
        {#if runtimeSetupOffered(preflight.runtimes)}
          {#if transport.mode === 'wasm'}
            <a
              href="/nodes"
              class="inline-flex h-7 shrink-0 items-center rounded-md border border-border bg-background px-2.5 text-xs font-medium transition-colors hover:bg-secondary"
            >
              Set up
            </a>
          {:else}
            <a
              href="/setup?step=vm"
              onclick={(e) => {
                // A new tab lands with no autostart, so it needs a click there.
                if (opensInNewTab(e)) return;
                e.preventDefault();
                goto('/setup?step=vm', { state: { autostartSetup: true } });
              }}
              class="inline-flex h-7 shrink-0 items-center rounded-md border border-border bg-background px-2.5 text-xs font-medium transition-colors hover:bg-secondary"
            >
              Set up
            </a>
          {/if}
        {/if}
        <button
          onclick={retryPreflight}
          disabled={retrying}
          class="inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md border border-border bg-background px-2.5 text-xs font-medium transition-colors hover:bg-secondary disabled:opacity-60"
        >
          {#if retrying}<Loader2 class="size-3.5 animate-spin" />{/if}
          Retry
        </button>
      </div>
    {/if}

    <div class="flex flex-1 overflow-hidden">
      <!-- Desktop sidebar: permanent left aside at md+. Hidden on
         mobile, where the same Sidebar component renders inside a
         Sheet below instead. -->
      <Sidebar class="hidden w-56 border-r border-border bg-card md:flex" />
      <main class="flex-1 overflow-auto">
        {@render children()}
      </main>
    </div>
  </div>

  <!-- Mobile sidebar: same component, slide-in drawer triggered by
     the topbar hamburger. Sheet handles overlay, ESC, focus trap. -->
  <Sheet bind:open={sidebarOpen}>
    <SheetContent side="left" class="w-64 bg-card p-0">
      <Sidebar class="h-full" />
    </SheetContent>
  </Sheet>

  <NewSessionDialog bind:open={newSessionOpen} />
{/if}
