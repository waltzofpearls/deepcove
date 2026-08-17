<script lang="ts">
  import { mode, setMode, userPrefersMode } from 'mode-watcher';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { api } from '$lib/api/client';
  import TailnetConnect from '$lib/components/TailnetConnect.svelte';
  import { Button } from '$lib/components/ui/button';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import Check from '@lucide/svelte/icons/check';

  // Refresh-interval choices for the preview/session pollers. Kept to a
  // few sensible steps rather than a free number field, for fewer knobs.
  const intervals = [
    { ms: 2000, label: '2 seconds' },
    { ms: 5000, label: '5 seconds' },
    { ms: 10000, label: '10 seconds' },
    { ms: 30000, label: '30 seconds' },
  ];

  const themes = [
    { value: 'system', label: 'System' },
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ] as const;

  let version = $state<string>('');
  $effect(() => {
    let cancelled = false;
    api
      .health()
      .then((h) => {
        if (!cancelled) version = h.version;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  });

  const selectClass =
    'flex h-9 w-full max-w-xs rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring';
  const inputClass =
    'h-9 w-40 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring';

  // Cluster (SPEC §5.12): this node's self-declared group label. Read on load, changed live.
  let currentCluster = $state('');
  let clusterDraft = $state('');
  let savingCluster = $state(false);
  let clusterSaved = $state(false);
  let clusterError = $state('');

  $effect(() => {
    let cancelled = false;
    api
      .getCluster()
      .then((r) => {
        if (!cancelled) {
          currentCluster = r.cluster;
          clusterDraft = r.cluster;
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  });

  // Suggestions for the type-or-pick field: clusters already seen across your nodes' sessions, plus the
  // default and current. Subscribing populates sessions.list so the suggestions have something to offer.
  $effect(() => sessions.subscribe());
  const knownClusters = $derived.by(() => {
    const set = new Set<string>(['default']);
    if (currentCluster) set.add(currentCluster);
    for (const s of sessions.list) if (s.host?.cluster) set.add(s.host.cluster);
    return [...set].sort();
  });

  async function saveCluster() {
    savingCluster = true;
    clusterError = '';
    try {
      const r = await api.setCluster(clusterDraft.trim());
      currentCluster = r.cluster;
      clusterDraft = r.cluster;
      clusterSaved = true;
      setTimeout(() => (clusterSaved = false), 1500);
    } catch (e) {
      clusterError = e instanceof Error ? e.message : String(e);
    } finally {
      savingCluster = false;
    }
  }
</script>

<div class="mx-auto max-w-5xl p-6">
  <div class="mb-6 flex items-center gap-3">
    <a
      href="/"
      class="flex size-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
      aria-label="Back to dashboard"
    >
      <ArrowLeft class="size-4" />
    </a>
    <h1 class="text-lg font-semibold">Settings</h1>
  </div>

  <!-- Two columns once there is room, one below md. Cards keep each group readable at any width; a full
       width row leaves the label an inch from its control. -->
  <div class="grid gap-4 md:grid-cols-2">
    <!-- Appearance -->
    <section class="rounded-xl border border-border bg-card p-4">
      <h2 class="mb-3 text-sm font-medium text-muted-foreground">Appearance</h2>
      <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:gap-4">
        <div class="min-w-0">
          <div class="text-sm font-medium">Theme</div>
          <div class="text-xs text-muted-foreground">
            {userPrefersMode.current === 'system'
              ? `Following the system (currently ${mode.current}).`
              : `Always ${userPrefersMode.current}.`}
          </div>
        </div>
        <div class="inline-flex rounded-md border border-border p-0.5">
          {#each themes as t (t.value)}
            <button
              onclick={() => setMode(t.value)}
              class="rounded px-3 py-1 text-xs font-medium transition-colors"
              class:bg-secondary={userPrefersMode.current === t.value}
              class:text-foreground={userPrefersMode.current === t.value}
              class:text-muted-foreground={userPrefersMode.current !== t.value}
            >
              {t.label}
            </button>
          {/each}
        </div>
      </div>
    </section>

    <!-- Dashboard -->
    <section class="space-y-4 rounded-xl border border-border bg-card p-4">
      <h2 class="text-sm font-medium text-muted-foreground">Dashboard</h2>

      <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:gap-4">
        <div class="min-w-0">
          <div class="text-sm font-medium">Default view</div>
          <div class="text-xs text-muted-foreground">How sessions are laid out.</div>
        </div>
        <select bind:value={preferences.viewMode} class={selectClass}>
          <option value="cards">Cards</option>
          <option value="list">List</option>
        </select>
      </div>

      <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:gap-4">
        <div class="min-w-0">
          <div class="text-sm font-medium">Refresh interval</div>
          <div class="text-xs text-muted-foreground">
            How often the dashboard and previews poll for updates.
          </div>
        </div>
        <select bind:value={preferences.pollIntervalMs} class={selectClass}>
          {#each intervals as i (i.ms)}
            <option value={i.ms}>{i.label}</option>
          {/each}
        </select>
      </div>
    </section>

    <!-- Access -->
    <TailnetConnect />

    <!-- Cluster -->
    <section class="space-y-3 rounded-xl border border-border bg-card p-4">
      <h2 class="text-sm font-medium text-muted-foreground">Cluster</h2>
      <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start sm:gap-4">
        <div class="min-w-0">
          <div class="text-sm font-medium">Cluster name</div>
          <div class="text-xs text-muted-foreground">
            Group this node with your others. Nodes that share a name are shown together on the
            dashboard. Leave it as <span class="font-mono">default</span> if you don't need groups.
          </div>
        </div>
        <div class="flex shrink-0 items-center gap-2 sm:justify-self-end">
          <input
            bind:value={clusterDraft}
            list="known-clusters"
            maxlength="64"
            placeholder="default"
            class={inputClass}
          />
          <datalist id="known-clusters">
            {#each knownClusters as c (c)}<option value={c}></option>{/each}
          </datalist>
          <Button
            size="sm"
            onclick={saveCluster}
            disabled={savingCluster || clusterDraft.trim() === currentCluster}
          >
            {#if savingCluster}
              <Loader2 class="size-4 animate-spin" />
            {:else if clusterSaved}
              <Check class="size-4" />
            {/if}
            Save
          </Button>
        </div>
      </div>
      {#if clusterError}
        <p class="text-xs text-destructive">{clusterError}</p>
      {/if}
    </section>

    <!-- About -->
    <section class="space-y-3 rounded-xl border border-border bg-card p-4">
      <h2 class="text-sm font-medium text-muted-foreground">About</h2>
      <div class="flex items-center justify-between gap-4 text-sm">
        <span class="font-medium">deepcove</span>
        <span class="font-mono text-xs text-muted-foreground">{version || 'unknown'}</span>
      </div>
    </section>
  </div>
</div>
