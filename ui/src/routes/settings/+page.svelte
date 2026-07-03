<script lang="ts">
  import { mode, setMode, userPrefersMode } from 'mode-watcher';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { api } from '$lib/api/client';
  import TailnetConnect from '$lib/components/TailnetConnect.svelte';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';

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
</script>

<div class="mx-auto max-w-2xl p-6">
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

  <div class="space-y-8">
    <!-- Appearance -->
    <section class="space-y-3">
      <h2 class="text-sm font-medium text-muted-foreground">Appearance</h2>
      <div class="flex items-center justify-between gap-4">
        <div>
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
    <section class="space-y-4">
      <h2 class="text-sm font-medium text-muted-foreground">Dashboard</h2>

      <div class="flex items-center justify-between gap-4">
        <div>
          <div class="text-sm font-medium">Default view</div>
          <div class="text-xs text-muted-foreground">How sessions are laid out.</div>
        </div>
        <select bind:value={preferences.viewMode} class={selectClass}>
          <option value="cards">Cards</option>
          <option value="list">List</option>
        </select>
      </div>

      <div class="flex items-center justify-between gap-4">
        <div>
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

    <!-- About -->
    <section class="space-y-3">
      <h2 class="text-sm font-medium text-muted-foreground">About</h2>
      <div class="flex items-center justify-between gap-4 text-sm">
        <span class="font-medium">deepcove</span>
        <span class="font-mono text-xs text-muted-foreground">{version || 'unknown'}</span>
      </div>
    </section>
  </div>
</div>
