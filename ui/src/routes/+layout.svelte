<script lang="ts">
  import '../app.css';

  import { page } from '$app/state';
  import { ModeWatcher } from 'mode-watcher';
  import TopBar from '$lib/components/layout/TopBar.svelte';
  import Sidebar from '$lib/components/layout/Sidebar.svelte';
  import NewSessionDialog from '$lib/components/session/NewSessionDialog.svelte';
  import { Sheet, SheetContent } from '$lib/components/ui/sheet';

  let { children } = $props();
  let newSessionOpen = $state(false);
  // Mobile-only: tracks the slide-in sidebar drawer. Desktop renders
  // the sidebar as a permanent left aside and ignores this.
  let sidebarOpen = $state(false);

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

<div class="flex h-screen flex-col bg-background text-foreground">
  <TopBar
    onNewSession={() => (newSessionOpen = true)}
    onMenu={() => (sidebarOpen = true)}
  />
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
