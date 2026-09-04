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
  import Info from '@lucide/svelte/icons/info';
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';

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

  // Hover shows the detail, a click or tap pins it, which is the only way it works on a phone.
  let proxyHelpOpen = $state(false);

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

  let currentProxy = $state('');
  let proxyDraft = $state('');
  let savingProxy = $state(false);
  let proxySaved = $state(false);
  let proxyError = $state('');

  $effect(() => {
    let cancelled = false;
    api
      .llmProxy()
      .then((p) => {
        if (cancelled) return;
        currentProxy = p.url;
        proxyDraft = p.url;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  });

  let testingProxy = $state(false);
  let proxyTest = $state('');

  // A reachability check from this node, which is the machine that will actually make the calls.
  async function testProxy() {
    testingProxy = true;
    proxyTest = '';
    proxyError = '';
    try {
      proxyTest = (await api.testLLMProxy()).result;
    } catch (e) {
      proxyError = e instanceof Error ? e.message : String(e);
    } finally {
      testingProxy = false;
    }
  }

  async function saveProxy() {
    savingProxy = true;
    proxyError = '';
    try {
      const p = await api.saveLLMProxy(proxyDraft.trim());
      currentProxy = p.url;
      proxyDraft = p.url;
      proxySaved = true;
      setTimeout(() => (proxySaved = false), 1500);
    } catch (e) {
      proxyError = e instanceof Error ? e.message : String(e);
    } finally {
      savingProxy = false;
    }
  }

  $effect(() => {
    let cancelled = false;
    return () => {
      cancelled = true;
    };
  });
</script>

{#snippet help(open: boolean, setOpen: (v: boolean) => void, text: string)}
  <TooltipProvider>
    <Tooltip {open} onOpenChange={setOpen} disableCloseOnTriggerClick>
      <TooltipTrigger>
        {#snippet child({ props })}
          <button
            {...props}
            type="button"
            onclick={() => setOpen(!open)}
            class="text-muted-foreground transition-colors hover:text-foreground"
            aria-label="More about this setting"
          >
            <Info class="size-3.5" />
          </button>
        {/snippet}
      </TooltipTrigger>
      <TooltipContent class="max-w-xs">{text}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
{/snippet}

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

    <section class="space-y-3 rounded-xl border border-border bg-card p-4">
      <div class="flex items-center gap-1.5">
        <h2 class="text-sm font-medium text-muted-foreground">LLM proxy</h2>
        {@render help(
          proxyHelpOpen,
          (v) => (proxyHelpOpen = v),
          'Leave it blank and they go direct. Machines already running follow this; a session has to be told to use it, in its settings.',
        )}
      </div>
      <p class="text-xs text-muted-foreground">
        Agents send their requests here instead of straight to the model's API, so the sandbox holds
        no key of its own.
      </p>
      <div class="flex items-center gap-2">
        <input
          bind:value={proxyDraft}
          placeholder="http://100.75.35.244:9001"
          class="{inputClass} min-w-0 flex-1"
        />
        <Button
          size="sm"
          variant="ghost"
          onclick={testProxy}
          disabled={testingProxy || !currentProxy}
        >
          {#if testingProxy}
            <Loader2 class="size-4 animate-spin" />
          {/if}
          Test
        </Button>
        <Button
          size="sm"
          onclick={saveProxy}
          disabled={savingProxy || proxyDraft.trim() === currentProxy}
        >
          {#if savingProxy}
            <Loader2 class="size-4 animate-spin" />
          {:else if proxySaved}
            <Check class="size-4" />
          {/if}
          Save
        </Button>
      </div>
      {#if proxyError}
        <p class="text-xs text-destructive">{proxyError}</p>
      {/if}
      {#if proxyTest}
        <p class="text-xs text-muted-foreground">{proxyTest}</p>
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
