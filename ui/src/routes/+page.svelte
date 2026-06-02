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

  // Subscribe to the shared sessions store. First subscriber kicks
  // off polling; cleanup is automatic on unmount via the $effect
  // return. The sidebar's session submenu and (on mobile) the
  // topbar tab carousel will share this same store.
  $effect(() => sessions.subscribe());

  const openNewSession = getContext<() => void>(NEW_SESSION_CTX);
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
  <!-- First-run / empty state: orient the user and give them one clear
       action. When Docker is down, surface that here too — creating a
       session won't work until it's back. -->
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
{:else if preferences.viewMode === 'cards'}
  <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
    {#each sessions.list as session (session.id)}
      <SessionCard {session} />
    {/each}
  </div>
{:else}
  <SessionListView sessions={sessions.list} />
{/if}
