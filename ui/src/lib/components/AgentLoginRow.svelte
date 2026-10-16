<script lang="ts" module>
  // The card's header and every row share these columns, so they line up like one table.
  export const loginRowColumns =
    'sm:grid-cols-[minmax(0,3fr)_minmax(0,4fr)_minmax(0,3fr)_minmax(0,3.5fr)]';
</script>

<script lang="ts">
  // One node's row in an agent's login card. The login, the default and the API key all live on that
  // node, so every read and save goes to it.
  import { untrack } from 'svelte';
  import { api } from '$lib/api/client';
  import { nodeHost } from '$lib/api/sessions';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { usePolling } from '$lib/polling';
  import OtherAccountChoice from '$lib/components/session/OtherAccountChoice.svelte';
  import { Button } from '$lib/components/ui/button';
  import { loginChoices, loginFallsBack } from '$lib/util/loginMode';
  import { machineName } from '$lib/util/tailnet';
  import type { Agent, LoginMode, NodeInfo, SharedLoginStatus } from '$lib/api/types';

  let { agent, node }: { agent: Agent; node: NodeInfo } = $props();

  let status = $state<SharedLoginStatus | null>(null);
  let loadError = $state('');
  let saving = $state(false);
  let saveError = $state('');
  let apiKeyDraft = $state('');

  // The node list is rebuilt on every poll, so the polling below keys on these values, not the object.
  const nodeKey = $derived(`${node.stable_node_id}|${node.dns_name}|${node.online}`);
  // This node rides the transport that served the page, like the Nodes page does.
  const host = $derived.by(() => {
    void nodeKey;
    return untrack(() => (node.self ? undefined : nodeHost(node)));
  });

  // The default offers what a session VM could use, since that is where most sessions run.
  let llmProxyUrl = $state('');
  const defaultChoices = $derived(
    status
      ? loginChoices(agent, 'session_vm', {
          proxyUrl: llmProxyUrl,
          hasApiKey: status.has_api_key,
          apiKeyEnding: status.api_key_ending,
        })
      : [],
  );

  // A default that is not set up any more falls back to the session's own login, as the node does.
  const defaultFallsBack = $derived(
    !!status && loginFallsBack(status.default_login_mode, defaultChoices),
  );
  const fallbackNote = $derived(
    status?.default_login_mode === 'api_key'
      ? 'The API key was removed, so new sessions use their own login.'
      : 'This node has no LLM proxy now, so new sessions use their own login.',
  );

  const nodeName = $derived(machineName(node.dns_name) || node.hostname);
  // A remote node with no address would fall back to this node, so it is shown but never asked.
  const reachable = $derived(node.self || !!node.dns_name);
  const account = $derived(
    !status?.logged_in ? 'Nobody yet' : status.account || `One ${agent.display_name} login`,
  );

  async function save(settings: { default_login_mode?: LoginMode; api_key?: string }) {
    saving = true;
    saveError = '';
    try {
      status = await api.saveLoginSettings(agent.name, settings, host);
      apiKeyDraft = '';
    } catch (e) {
      saveError = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    void nodeKey;
    const online = untrack(() => node.online && reachable);
    const target = host;
    if (!online) return;
    const ac = new AbortController();
    const stop = usePolling(
      async () => {
        try {
          status = await api.sharedLogin(agent.name, target, ac.signal);
          loadError = '';
        } catch (e) {
          if ((e as Error)?.name !== 'AbortError') {
            loadError = e instanceof Error ? e.message : String(e);
          }
        }
        // Read on every round too: the node's LLM proxy can be added or removed at any time.
        try {
          llmProxyUrl = (await api.llmProxy(target, ac.signal)).url ?? '';
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
</script>

<div class="grid gap-2 border-t border-border pt-3 sm:items-center sm:gap-4 {loginRowColumns}">
  <div class="min-w-0">
    <div class="truncate text-sm font-medium" title={nodeName}>{nodeName}</div>
    {#if node.self}<div class="text-xs text-muted-foreground">This machine</div>{/if}
  </div>
  {#if !node.online || !reachable}
    <p class="text-xs text-muted-foreground sm:col-span-3">
      {node.online ? "Can't reach this node" : 'Offline'}
    </p>
  {:else if status === null}
    <p class="text-xs sm:col-span-3" class:text-destructive={!!loadError}>
      {loadError || 'Loading…'}
    </p>
  {:else}
    <div class="min-w-0 truncate text-sm">{account}</div>
    <div class="min-w-0 space-y-1">
      <div class="text-xs text-muted-foreground sm:hidden">New sessions use</div>
      <select
        value={defaultFallsBack ? 'in_session' : status.default_login_mode}
        disabled={saving}
        onchange={(e) => save({ default_login_mode: e.currentTarget.value as LoginMode })}
        class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs"
        aria-label={`How new sessions on ${nodeName} log in`}
      >
        {#each defaultChoices as choice (choice.mode)}
          <option value={choice.mode} disabled={!!choice.unavailable}>{choice.label}</option>
        {/each}
      </select>
      {#if defaultFallsBack}<div class="text-xs text-warning">{fallbackNote}</div>{/if}
    </div>
    <div class="min-w-0">
      {#if status.has_api_key}
        <div class="flex items-center justify-between gap-2">
          <span class="text-sm text-muted-foreground">
            <span class="sm:hidden">API key</span>
            {status.api_key_ending ? `•••• ${status.api_key_ending}` : 'Saved'}
          </span>
          <Button size="sm" variant="ghost" disabled={saving} onclick={() => save({ api_key: '' })}>
            Remove
          </Button>
        </div>
      {:else}
        <div class="flex items-center gap-2">
          <input
            type="password"
            bind:value={apiKeyDraft}
            placeholder={agent.api_key_example}
            autocomplete="off"
            class="h-9 min-w-0 flex-1 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs"
            aria-label={`${agent.display_name} API key for ${nodeName}`}
          />
          <Button
            size="sm"
            disabled={saving || !apiKeyDraft.trim()}
            onclick={() => save({ api_key: apiKeyDraft })}
          >
            Save
          </Button>
        </div>
      {/if}
    </div>
    {#if status.waiting}
      <div
        class="space-y-2 rounded-md border border-border bg-primary/5 p-3 sm:col-span-3 sm:col-start-2"
      >
        <p class="text-sm">
          {`A session logged in as ${status.waiting.account || 'another account'}.`}
        </p>
        <OtherAccountChoice
          agent={agent.name}
          {host}
          {status}
          onanswered={(next) => (status = next)}
        />
      </div>
    {/if}
    {#if saveError}
      <p class="text-xs text-destructive sm:col-span-3 sm:col-start-2">{saveError}</p>
    {/if}
  {/if}
</div>
