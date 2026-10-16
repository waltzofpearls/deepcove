<script lang="ts">
  import { mode, setMode, userPrefersMode } from 'mode-watcher';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { api } from '$lib/api/client';
  import TailnetConnect from '$lib/components/TailnetConnect.svelte';
  import AgentLoginCard from '$lib/components/AgentLoginCard.svelte';
  import LLMProxyRow from '$lib/components/LLMProxyRow.svelte';
  import { discoveryRequired } from '$lib/api/transport';
  import { discovery } from '$lib/stores/discovery.svelte';
  import { usePolling } from '$lib/polling';
  import type { Agent, NodeInfo } from '$lib/api/types';
  import { Button } from '$lib/components/ui/button';
  import ArrowLeft from '@lucide/svelte/icons/arrow-left';
  import Check from '@lucide/svelte/icons/check';
  import Copy from '@lucide/svelte/icons/copy';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import Info from '@lucide/svelte/icons/info';
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';

  // One login card for each agent whose login deepcove can share or whose API key it can keep.
  let loginAgents = $state<Agent[]>([]);
  $effect(() => {
    api
      .listAgents()
      .then(
        (agents) =>
          (loginAgents = agents.filter((agent) =>
            agent.login_modes.some((mode) => mode === 'shared' || mode === 'api_key'),
          )),
      )
      .catch(() => {});
  });

  // Logins live on each node, so the login cards list every node. Dashboard mode finds them itself.
  let fetchedNodes = $state<NodeInfo[]>([]);
  const nodes = $derived(discoveryRequired() ? discovery.nodes : fetchedNodes);
  $effect(() => {
    if (discoveryRequired()) return;
    const ac = new AbortController();
    const stop = usePolling(
      async () => {
        try {
          fetchedNodes = await api.nodes(ac.signal);
        } catch {
          // Kept as it was until the next round answers.
        }
      },
      () => preferences.pollIntervalMs,
    );
    return () => {
      ac.abort();
      stop();
    };
  });

  const upgradeCommand = 'deepcove upgrade';
  let upgradeCopied = $state(false);

  async function copyUpgradeCommand() {
    try {
      await navigator.clipboard.writeText(upgradeCommand);
      upgradeCopied = true;
      setTimeout(() => (upgradeCopied = false), 1500);
    } catch {
      // The clipboard needs a secure context. The command is still selectable.
    }
  }

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

    <!-- About -->
    <section class="space-y-3 rounded-xl border border-border bg-card p-4">
      <h2 class="text-sm font-medium text-muted-foreground">About</h2>
      <div class="flex items-center justify-between gap-4 text-sm">
        <span class="font-medium">deepcove</span>
        <span class="font-mono text-xs text-muted-foreground">{version || 'unknown'}</span>
      </div>
      <div class="space-y-2 border-t border-border pt-3">
        <div>
          <div class="text-sm font-medium">Update</div>
          <div class="text-xs text-muted-foreground">Run this on each machine.</div>
        </div>
        <div
          class="flex items-center gap-2 rounded-md border border-border bg-secondary p-2 font-mono text-xs"
        >
          <span class="flex-1 truncate">{upgradeCommand}</span>
          <Button
            size="icon"
            variant="ghost"
            class="size-7 shrink-0"
            onclick={copyUpgradeCommand}
            aria-label="Copy the update command"
          >
            {#if upgradeCopied}
              <Check class="size-3.5 text-[var(--color-success)]" />
            {:else}
              <Copy class="size-3.5" />
            {/if}
          </Button>
        </div>
      </div>
      <div class="flex items-center justify-between gap-4 border-t border-border pt-3 text-sm">
        <span class="font-medium">Website</span>
        <a
          href="https://deepcove.dev"
          target="_blank"
          rel="noopener"
          class="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:underline"
        >
          deepcove.dev
          <ExternalLink class="size-3" />
        </a>
      </div>
    </section>

    {#each loginAgents as agent (agent.name)}
      <AgentLoginCard {agent} {nodes} />
    {/each}

    <section class="space-y-3 rounded-xl border border-border bg-card p-4 md:col-span-2">
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
      {#each nodes as node (node.stable_node_id || node.hostname)}
        <LLMProxyRow {node} />
      {/each}
    </section>
  </div>
</div>
