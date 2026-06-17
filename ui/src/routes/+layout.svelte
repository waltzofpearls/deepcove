<script lang="ts">
  import '../app.css';

  import { onMount, setContext } from 'svelte';
  import { page } from '$app/state';
  import { ModeWatcher } from 'mode-watcher';
  import TopBar from '$lib/components/layout/TopBar.svelte';
  import Sidebar from '$lib/components/layout/Sidebar.svelte';
  import NewSessionDialog from '$lib/components/session/NewSessionDialog.svelte';
  import TailnetGate from '$lib/components/TailnetGate.svelte';
  import { transport } from '$lib/api/transport';
  import { ensureTailnet, tailnetAuth } from '$lib/api/wasm';
  import { Sheet, SheetContent } from '$lib/components/ui/sheet';
  import { preflight } from '$lib/stores/preflight.svelte';
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
  // touches this — `tailnetAuth` stays idle and `gated` is false.
  onMount(() => {
    if (transport.mode === 'wasm') void ensureTailnet().catch(() => {});
  });
  let gated = $derived(transport.mode === 'wasm' && $tailnetAuth.phase !== 'running');

  // Environment-readiness poller; powers the Docker-down banner below
  // and the New Session dialog's gating. Lazy + ref-counted.
  $effect(() => preflight.subscribe());

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
       in-browser node is running. The dashboard never mounts before then. -->
  <TailnetGate />
{:else}
<div class="flex h-screen flex-col bg-background text-foreground">
  <TopBar
    onNewSession={() => (newSessionOpen = true)}
    onMenu={() => (sidebarOpen = true)}
  />

  {#if preflight.loaded && !preflight.dockerReachable}
    <div
      class="flex items-center gap-3 border-b border-destructive/40 bg-destructive/10 px-4 py-2 text-sm text-foreground"
      role="alert"
    >
      <CircleAlert class="size-4 shrink-0 text-destructive" />
      <span class="min-w-0 flex-1">
        {preflight.docker.message || "Docker isn't running."}
        Sessions can't be created until it's back.
      </span>
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
         mobile — the same Sidebar component renders inside a Sheet
         below instead. -->
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
