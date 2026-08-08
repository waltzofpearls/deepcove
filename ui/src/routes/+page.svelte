<script lang="ts">
  import { getContext } from 'svelte';
  import { goto } from '$app/navigation';
  import { transport } from '$lib/api/transport';
  import { isSetupDone, markSetupDone, isConnectSkipped, setConnectSkipped } from '$lib/setup';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { connect } from '$lib/stores/connect.svelte';
  import { NEW_SESSION_CTX } from '$lib/context';
  import type { Session } from '$lib/api/types';
  import { DEFAULT_CLUSTER } from '$lib/util/cluster';
  import SessionCard from '$lib/components/session/SessionCard.svelte';
  import SessionListView from '$lib/components/session/SessionListView.svelte';
  import Boxes from '@lucide/svelte/icons/boxes';
  import Layers from '@lucide/svelte/icons/layers';
  import Plus from '@lucide/svelte/icons/plus';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';
  import Info from '@lucide/svelte/icons/info';
  import X from '@lucide/svelte/icons/x';
  import Search from '@lucide/svelte/icons/search';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';

  // Subscribe to the shared sessions store. First subscriber kicks
  // off polling; cleanup is automatic on unmount via the $effect
  // return.
  $effect(() => sessions.subscribe());

  const openNewSession = getContext<() => void>(NEW_SESSION_CTX);

  // First-run: a fresh session node (direct mode, no sessions, setup not done) goes to the setup
  // wizard. An existing install (has sessions) is marked done so it never nags. Dashboard (wasm) mode
  // skips this, since its onboarding is the sign-in gate, not the wizard.
  $effect(() => {
    if (transport.mode !== 'direct' || sessions.loading) return;
    if (sessions.list.length > 0) {
      markSetupDone();
    } else if (!isSetupDone()) {
      void goto('/setup');
    }
  });

  // Skipped-step nudge: if the wizard was finished without connecting the session node, suggest
  // Tailscale for remote/multi-machine access. Auto-clears once connected; dismissable.
  $effect(() => connect.subscribe());
  let connectSkipped = $state(isConnectSkipped());
  $effect(() => {
    if (connect.connected && connectSkipped) {
      setConnectSkipped(false);
      connectSkipped = false;
    }
  });
  const showConnectNudge = $derived(
    transport.mode === 'direct' && connectSkipped && !connect.connected,
  );
  function dismissNudge() {
    setConnectSkipped(false);
    connectSkipped = false;
  }

  // Client-side filtering. The store already holds every session
  // (it lists with all=true), so search/status filtering is a cheap
  // local derive, no extra requests.
  let query = $state('');
  let statusFilter = $state<'all' | 'running'>('all');

  let refreshing = $state(false);
  async function refreshDashboard() {
    refreshing = true;
    try {
      await sessions.refresh();
    } finally {
      refreshing = false;
    }
  }

  const filtered = $derived.by(() => {
    const q = query.trim().toLowerCase();
    return sessions.list.filter((s) => {
      if (statusFilter === 'running' && s.status !== 'running') return false;
      if (!q) return true;
      return s.name.toLowerCase().includes(q) || s.agent.toLowerCase().includes(q);
    });
  });

  // Group sessions by host-node cluster (SPEC §5.12); chrome only appears with more than one cluster.
  const clusterOf = (s: Session) => s.host?.cluster || DEFAULT_CLUSTER;
  // From the FULL list, not `filtered`: a transient empty search would otherwise wipe the persisted filter.
  const allClusters = $derived([...new Set(sessions.list.map(clusterOf))].sort());
  const showClusters = $derived(allClusters.length > 1);

  // Reset the persisted filter only when the selected cluster no longer exists at all.
  $effect(() => {
    if (preferences.clusterFilter !== 'all' && !allClusters.includes(preferences.clusterFilter)) {
      preferences.clusterFilter = 'all';
    }
  });

  const visible = $derived(
    preferences.clusterFilter === 'all'
      ? filtered
      : filtered.filter((s) => clusterOf(s) === preferences.clusterFilter),
  );
  // Sessions grouped by cluster (name-sorted) for the "all clusters" overview.
  const grouped = $derived.by(() => {
    const m = new Map<string, Session[]>();
    for (const s of filtered) {
      const c = clusterOf(s);
      const arr = m.get(c);
      if (arr) arr.push(s);
      else m.set(c, [s]);
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b));
  });
</script>

{#if showConnectNudge}
  <!-- Informational only: deepcove works fine local, this just surfaces the optional Tailscale upside. -->
  <div
    class="flex items-center gap-3 border-b border-blue-300 bg-blue-50 px-4 py-2.5 text-sm text-blue-900 dark:border-blue-400/25 dark:bg-blue-500/10 dark:text-blue-100"
    role="status"
  >
    <Info class="size-4 shrink-0 text-blue-600 dark:text-blue-400" />
    <span class="min-w-0 flex-1">
      Reach your sessions remotely, or run them across more than one machine.
      <a
        href="/setup?step=connect"
        class="font-medium text-blue-700 underline underline-offset-2 hover:text-blue-900 dark:text-blue-300 dark:hover:text-blue-100"
      >
        Resume setup
      </a>
    </span>
    <button
      class="rounded p-1 text-blue-700 transition-colors hover:bg-blue-500/15 dark:text-blue-300"
      onclick={dismissNudge}
      aria-label="Dismiss"
    >
      <X class="size-4" />
    </button>
  </div>
{/if}

{#if sessions.error}
  <div class="flex h-full items-center justify-center p-8">
    <div class="text-center text-sm text-destructive">{sessions.error}</div>
  </div>
{:else if sessions.loading}
  <!-- Skeletons rather than a bare "Loading…" line so the layout
       doesn't jump when cards arrive. -->
  <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
    {#each Array(16) as _, i (i)}
      <div
        class="flex aspect-[4/3] animate-pulse flex-col overflow-hidden rounded-lg border border-border bg-card"
      >
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
       action. When nothing can host a session, surface that here too. -->
  <div class="flex h-full items-center justify-center p-8">
    <div class="flex max-w-md flex-col items-center text-center">
      <div class="mb-4 rounded-2xl bg-secondary p-4">
        <Boxes class="size-8 text-muted-foreground" />
      </div>
      <h2 class="text-lg font-semibold">Run a coding agent in its own sandbox</h2>
      <p class="mt-1.5 text-sm text-muted-foreground">
        Each session is an isolated sandbox running Claude Code, reachable from this dashboard.
        Create one to get started.
      </p>

      {#if !preflight.ready}
        <div
          class="mt-5 flex items-start gap-2.5 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-left text-sm"
        >
          <CircleAlert class="mt-0.5 size-4 shrink-0 text-destructive" />
          <span>Nothing on this machine can host a session yet.</span>
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
  {#if sessions.unreachable.length > 0}
    <!-- A node is on the tailnet but its deepcove API didn't answer this poll; surface that its
         sessions are omitted rather than letting the list look complete. -->
    <div
      class="mx-6 mt-6 flex items-center gap-2.5 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-sm"
    >
      <CircleAlert class="size-4 shrink-0 text-warning" />
      <span>
        Couldn't reach {sessions.unreachable.map((n) => n.hostname).join(', ')}; its sessions aren't
        shown.
      </span>
    </div>
  {/if}
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
    {#if showClusters}
      <select
        bind:value={preferences.clusterFilter}
        class="h-9 shrink-0 rounded-md border border-input bg-transparent px-3 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
        aria-label="Filter by cluster"
      >
        <option value="all">All clusters</option>
        {#each allClusters as c (c)}<option value={c}>{c}</option>{/each}
      </select>
    {/if}
    <button
      onclick={refreshDashboard}
      disabled={refreshing}
      aria-label="Refresh"
      class="flex size-9 shrink-0 items-center justify-center rounded-md border border-input text-muted-foreground shadow-xs transition-colors hover:bg-secondary hover:text-foreground disabled:opacity-50"
    >
      <RefreshCw class="size-4 {refreshing ? 'animate-spin' : ''}" />
    </button>
  </div>

  {#if filtered.length === 0}
    <div class="p-8 text-center text-sm text-muted-foreground">No sessions match your filters.</div>
  {:else if showClusters && preferences.clusterFilter === 'all'}
    <!-- Grouped by cluster: a header per cluster, then that cluster's sessions. -->
    {#each grouped as [cluster, group] (cluster)}
      <div class="flex items-center gap-2 px-6 pb-1 pt-6">
        <Layers class="size-4 shrink-0 text-muted-foreground" />
        <h3 class="text-sm font-semibold">{cluster}</h3>
        <span class="text-xs text-muted-foreground">{group.length}</span>
        <div class="ml-1 h-px flex-1 bg-border"></div>
      </div>
      {#if preferences.viewMode === 'cards'}
        <div class="grid grid-cols-1 gap-4 px-6 pb-2 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
          {#each group as session (session.id)}
            <SessionCard {session} />
          {/each}
        </div>
      {:else}
        <SessionListView sessions={group} />
      {/if}
    {/each}
    <div class="pb-4"></div>
  {:else if preferences.viewMode === 'cards'}
    <div class="grid grid-cols-1 gap-4 p-6 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
      {#each visible as session (session.id)}
        <SessionCard {session} />
      {/each}
    </div>
  {:else}
    <div class="pt-4">
      <SessionListView sessions={visible} />
    </div>
  {/if}
{/if}
