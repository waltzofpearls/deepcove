<script lang="ts">
  import { api, ApiError } from '$lib/api/client';
  import type { Session } from '$lib/api/types';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { usePolling } from '$lib/polling';
  import SessionCard from '$lib/components/session/SessionCard.svelte';
  import SessionListView from '$lib/components/session/SessionListView.svelte';

  let sessions = $state<Session[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);

  // Poll the session list on the same cadence as previews so newly
  // created sessions show up promptly and activity-reordered cards
  // animate into their new positions.
  $effect(() =>
    usePolling(
      async () => {
        try {
          sessions = await api.listSessions({ all: true });
          loadError = null;
        } catch (e) {
          loadError = e instanceof ApiError ? e.message : String(e);
        } finally {
          loading = false;
        }
      },
      () => preferences.pollIntervalMs
    )
  );
</script>

{#if loadError}
  <div class="flex h-full items-center justify-center p-8">
    <div class="text-center text-sm text-destructive">{loadError}</div>
  </div>
{:else if loading}
  <div class="flex h-full items-center justify-center p-8 text-muted-foreground">
    <span class="text-sm">Loading sessions…</span>
  </div>
{:else if sessions.length === 0}
  <div class="flex h-full items-center justify-center p-8 text-muted-foreground">
    <div class="text-center">
      <p class="text-sm">No sessions yet.</p>
      <p class="mt-1 text-xs">Click "New session" to get started.</p>
    </div>
  </div>
{:else if preferences.viewMode === 'cards'}
  <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
    {#each sessions as session (session.id)}
      <SessionCard {session} />
    {/each}
  </div>
{:else}
  <SessionListView {sessions} />
{/if}
