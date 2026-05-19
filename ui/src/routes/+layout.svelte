<script lang="ts">
  import '../app.css';

  import { ModeWatcher } from 'mode-watcher';
  import TopBar from '$lib/components/layout/TopBar.svelte';
  import Sidebar from '$lib/components/layout/Sidebar.svelte';
  import NewSessionSheet from '$lib/components/session/NewSessionSheet.svelte';

  let { children } = $props();
  let newSessionOpen = $state(false);
</script>

<!-- ModeWatcher applies the .dark class on <html> based on system pref
     + user override stored in localStorage. Defaults to system. -->
<ModeWatcher defaultMode="system" />

<div class="flex h-screen flex-col bg-background text-foreground">
  <TopBar onNewSession={() => (newSessionOpen = true)} />
  <div class="flex flex-1 overflow-hidden">
    <Sidebar />
    <main class="flex-1 overflow-auto">
      {@render children()}
    </main>
  </div>
</div>

<NewSessionSheet bind:open={newSessionOpen} />
