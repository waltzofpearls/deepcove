<script lang="ts">
  import { preferences } from '$lib/stores/preferences.svelte';
  import { sessions } from '$lib/stores/sessions.svelte';
  import SessionCard from '$lib/components/session/SessionCard.svelte';
  import SessionListView from '$lib/components/session/SessionListView.svelte';

  // Subscribe to the shared sessions store. First subscriber kicks
  // off polling; cleanup is automatic on unmount via the $effect
  // return. The sidebar's session submenu and (on mobile) the
  // topbar tab carousel will share this same store.
  $effect(() => sessions.subscribe());
</script>

{#if sessions.error}
  <div class="flex h-full items-center justify-center p-8">
    <div class="text-center text-sm text-destructive">{sessions.error}</div>
  </div>
{:else if sessions.loading}
  <div class="flex h-full items-center justify-center p-8 text-muted-foreground">
    <span class="text-sm">Loading sessions…</span>
  </div>
{:else if sessions.list.length === 0}
  <div class="flex h-full items-center justify-center p-8 text-muted-foreground">
    <div class="text-center">
      <p class="text-sm">No sessions yet.</p>
      <p class="mt-1 text-xs">Click "New session" to get started.</p>
    </div>
  </div>
{:else if preferences.viewMode === 'cards'}
  <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
    {#each sessions.list as session (session.id)}
      <SessionCard {session} />
    {/each}
  </div>
{:else}
  <SessionListView sessions={sessions.list} />
{/if}
