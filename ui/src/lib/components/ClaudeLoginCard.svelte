<script lang="ts">
  // The Settings card for this node's Claude login: the shared account, the default, and the API key.
  import { api } from '$lib/api/client';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { usePolling } from '$lib/polling';
  import OtherAccountChoice from '$lib/components/session/OtherAccountChoice.svelte';
  import { Button } from '$lib/components/ui/button';
  import { loginChoices, loginFallsBack } from '$lib/util/loginMode';
  import type { LoginMode, SharedLoginStatus } from '$lib/api/types';

  let status = $state<SharedLoginStatus | null>(null);
  let loadError = $state('');
  let saving = $state(false);
  let saveError = $state('');
  let apiKeyDraft = $state('');

  // The default offers what a session VM could use, since that is where most sessions run.
  let agentModes = $state<LoginMode[]>([]);
  let llmProxyUrl = $state('');
  $effect(() => {
    api
      .listAgents()
      .then((agents) => (agentModes = agents.find((a) => a.name === 'claude')?.login_modes ?? []))
      .catch(() => {});
  });
  const defaultChoices = $derived(
    status
      ? loginChoices(agentModes, 'session_vm', {
          proxyUrl: llmProxyUrl,
          hasApiKey: status.has_api_key,
          apiKeyEnding: status.api_key_ending,
        })
      : [],
  );

  // A default that is not set up any more falls back to the session's own login, as the node does.
  const defaultFallsBack = $derived(
    !!status && agentModes.length > 0 && loginFallsBack(status.default_login_mode, defaultChoices),
  );
  const fallbackNote = $derived(
    status?.default_login_mode === 'api_key'
      ? 'Your API key was removed, so new sessions use their own login.'
      : 'This node has no LLM proxy now, so new sessions use their own login.',
  );

  async function save(settings: { default_login_mode?: LoginMode; api_key?: string }) {
    saving = true;
    saveError = '';
    try {
      status = await api.saveLoginSettings('claude', settings);
      apiKeyDraft = '';
    } catch (e) {
      saveError = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  $effect(() => {
    const ac = new AbortController();
    const stop = usePolling(
      async () => {
        try {
          status = await api.sharedLogin('claude', undefined, ac.signal);
          loadError = '';
        } catch (e) {
          if ((e as Error)?.name !== 'AbortError') {
            loadError = e instanceof Error ? e.message : String(e);
          }
        }
        // Read on every round too: the LLM proxy card on this page can add or remove it at any time.
        try {
          llmProxyUrl = (await api.llmProxy(undefined, ac.signal)).url ?? '';
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

<section class="space-y-3 rounded-xl border border-border bg-card p-4">
  <h2 class="text-sm font-medium text-muted-foreground">Claude login</h2>
  {#if status === null}
    {#if loadError}
      <p class="text-xs text-destructive">{loadError}</p>
    {:else}
      <p class="text-xs text-muted-foreground">Loading…</p>
    {/if}
  {:else}
    {#if !status.logged_in}
      <p class="text-sm">
        Nobody has logged in yet. Log in once in any session and your other sessions use it too.
      </p>
    {:else if status.account}
      <p class="text-sm">{`Your sessions share ${status.account}.`}</p>
    {:else}
      <p class="text-sm">Your sessions share one Claude login.</p>
    {/if}
    <div
      class="grid gap-2 border-t border-border pt-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:gap-4"
    >
      <div class="min-w-0">
        <div class="text-sm font-medium">New sessions use</div>
        <div class="text-xs text-muted-foreground">Each session can change it in its settings.</div>
        {#if defaultFallsBack}<div class="text-xs text-warning">{fallbackNote}</div>{/if}
      </div>
      <select
        value={defaultFallsBack ? 'in_session' : status.default_login_mode}
        disabled={saving}
        onchange={(e) => save({ default_login_mode: e.currentTarget.value as LoginMode })}
        class="flex h-9 w-full max-w-xs rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs"
        aria-label="How new sessions log in"
      >
        {#each defaultChoices as choice (choice.mode)}
          <option value={choice.mode} disabled={!!choice.unavailable}>{choice.label}</option>
        {/each}
      </select>
    </div>
    <div class="space-y-2 border-t border-border pt-3">
      <div class="text-sm font-medium">API key</div>
      {#if status.has_api_key}
        <div class="flex items-center justify-between gap-2">
          <p class="text-xs text-muted-foreground">
            {status.api_key_ending
              ? `Saved key ending ${status.api_key_ending}. It never enters a session.`
              : 'A key is saved. It never enters a session.'}
          </p>
          <Button size="sm" variant="ghost" disabled={saving} onclick={() => save({ api_key: '' })}>
            Remove
          </Button>
        </div>
      {:else}
        <p class="text-xs text-muted-foreground">
          For sessions that log in with your API key. deepcove adds it to their requests, so it
          never enters a session.
        </p>
        <div class="flex items-center gap-2">
          <input
            type="password"
            bind:value={apiKeyDraft}
            placeholder="sk-ant-…"
            autocomplete="off"
            class="h-9 min-w-0 flex-1 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs"
            aria-label="Anthropic API key"
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
    {#if saveError}<p class="text-xs text-destructive">{saveError}</p>{/if}
    {#if status.waiting}
      <div class="space-y-2 rounded-md border border-border bg-primary/5 p-3">
        <p class="text-sm">
          {`A session logged in as ${status.waiting.account || 'another account'}.`}
        </p>
        <OtherAccountChoice agent="claude" {status} onanswered={(next) => (status = next)} />
      </div>
    {/if}
  {/if}
</section>
