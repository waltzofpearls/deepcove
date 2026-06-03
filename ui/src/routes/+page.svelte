<script lang="ts">
  import { getContext } from 'svelte';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { NEW_SESSION_CTX } from '$lib/context';
  import SessionCard from '$lib/components/session/SessionCard.svelte';
  import SessionListView from '$lib/components/session/SessionListView.svelte';
  import Boxes from '@lucide/svelte/icons/boxes';
  import Plus from '@lucide/svelte/icons/plus';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';
  import Search from '@lucide/svelte/icons/search';

  // Subscribe to the shared sessions store. First subscriber kicks
  // off polling; cleanup is automatic on unmount via the $effect
  // return.
  $effect(() => sessions.subscribe());

  const openNewSession = getContext<() => void>(NEW_SESSION_CTX);

  // Client-side filtering — the store already holds every session
  // (it lists with all=true), so search/status filtering is a cheap
  // local derive, no extra requests.
  let query = $state('');
  let statusFilter = $state<'all' | 'running'>('all');

  const filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    return sessions.list.filter((s) => {
      if (statusFilter === 'running' && s.status !== 'running') return false;
      if (!q) return true;
      return s.name.toLowerCase().includes(q) || s.agent.toLowerCase().includes(q);
    });
  });
</script>

{#if sessions.error}
  <div class="flex h-full items-center justify-center p-8">
    <div class="text-center text-sm text-destructive">{sessions.error}</div>
  </div>
{:else if sessions.loading}
  <!-- Skeletons rather than a bare "Loading…" line so the layout
       doesn't jump when cards arrive. -->
  <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
    {#each Array(4) as _, i (i)}
      <div class="flex aspect-[4/3] animate-pulse flex-col overflow-hidden rounded-lg border border-border bg-card">
        <div class="flex-1 bg-secondary/40"></div>
        <div class="flex items-center gap-2 border-t border-border px-3 py-2.5">
          <div class="size-2.5 rounded-full bg-secondary"></div>
          <div class="h-3 w-2/3 rounded bg-secondary"></div>
        </div>
      </div>
    {/each}
  </div>
{:else if sessions.list.length === 0}
  <!-- First-run / empty state: orient the user and give them one clear
       action. When Docker is down, surface that here too. -->
  <div class="flex h-full items-center justify-center p-8">
    <div class="flex max-w-md flex-col items-center text-center">
      <div class="mb-4 rounded-2xl bg-secondary p-4">
        <Boxes class="size-8 text-muted-foreground" />
      </div>
      <h2 class="text-lg font-semibold">Run a coding agent in its own sandbox</h2>
      <p class="mt-1.5 text-sm text-muted-foreground">
        Each session is an isolated container running Claude Code, reachable from
        this dashboard. Create one to get started.
      </p>

      {#if preflight.loaded && !preflight.dockerReachable}
        <div
          class="mt-5 flex items-start gap-2.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-left text-sm"
        >
          <CircleAlert class="mt-0.5 size-4 shrink-0 text-destructive" />
          <span>
            {preflight.docker.message || "Docker isn't running."}
            Start it, then create your first session.
          </span>
        </div>
      {:else}
        <button
          onclick={openNewSession}
          class="mt-5 inline-flex h-10 items-center gap-2 rounded-md bg-primary px-5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
        >
          <Plus class="size-4" />
          Create your first session
        </button>
      {/if}
    </div>
  </div>
{:else}
  <div class="flex flex-wrap items-center gap-3 px-6 pt-6">
    <div class="relative min-w-0 flex-1 sm:max-w-xs">
      <Search
        class="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
      />
      <input
        bind:value={query}
        placeholder="Search sessions…"
        class="h-9 w-full rounded-md border border-input bg-transparent pl-8 pr-3 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
      />
    </div>
    <div class="inline-flex shrink-0 rounded-md border border-border p-0.5">
      <button
        onclick={() => (statusFilter = 'all')}
        class="rounded px-3 py-1 text-xs font-medium transition-colors"
        class:bg-secondary={statusFilter === 'all'}
        class:text-foreground={statusFilter === 'all'}
        class:text-muted-foreground={statusFilter !== 'all'}
      >
        All
      </button>
      <button
        onclick={() => (statusFilter = 'running')}
        class="rounded px-3 py-1 text-xs font-medium transition-colors"
        class:bg-secondary={statusFilter === 'running'}
        class:text-foreground={statusFilter === 'running'}
        class:text-muted-foreground={statusFilter !== 'running'}
      >
        Running
      </button>
    </div>
  </div>

  {#if filtered.length === 0}
    <div class="p-8 text-center text-sm text-muted-foreground">
      No sessions match your filters.
    </div>
  {:else if preferences.viewMode === 'cards'}
    <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
      {#each filtered as session (session.id)}
        <SessionCard {session} />
      {/each}
    </div>
  {:else}
    <div class="pt-4">
      <SessionListView sessions={filtered} />
    </div>
  {/if}
{/if}
