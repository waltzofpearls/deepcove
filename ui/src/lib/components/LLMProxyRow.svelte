<script lang="ts">
  // One node's row in the LLM proxy card. Each node keeps its own proxy and makes the calls itself, so
  // the read, the save and the test all go to that node. The URL and the headers save together.
  import { untrack } from 'svelte';
  import { api, ApiError } from '$lib/api/client';
  import { nodeHost } from '$lib/api/sessions';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { usePolling } from '$lib/polling';
  import { Button } from '$lib/components/ui/button';
  import { machineName } from '$lib/util/tailnet';
  import {
    changeCount,
    hasIncompleteNewRow,
    headerEdits,
    newHostMessage,
    rowsFromSaved,
    rowsNeedingValueAgain,
    sameHeaderName,
    type HeaderRow,
  } from '$lib/util/llmProxyForm';
  import type { LLMProxy, LLMProxyTestResult, NodeInfo } from '$lib/api/types';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import Check from '@lucide/svelte/icons/check';
  import CircleCheck from '@lucide/svelte/icons/circle-check';
  import CircleX from '@lucide/svelte/icons/circle-x';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

  let { node }: { node: NodeInfo } = $props();

  let saved = $state<Required<LLMProxy> | null>(null);
  // Bumped on every load and save, so a test of the old saved values never shows after a save.
  let savedVersion = $state(0);
  let headersSupported = $state(true);
  let urlDraft = $state('');
  let rows = $state<HeaderRow[]>([]);
  let nextRowId = 0;
  let loadError = $state('');
  let saving = $state(false);
  let showSavedCheck = $state(false);
  let formError = $state('');
  let refusedHeader = $state<{ name: string; message: string } | null>(null);
  let testing = $state(false);
  // A result belongs to the form it tested, so an edit after it, or a slow answer after Discard, never
  // shows it beside values that were not tested.
  let testResult = $state<{ draft: string; result: LLMProxyTestResult } | null>(null);

  // The node list is rebuilt on every poll, so the load below keys on these values, not the object.
  const nodeKey = $derived(`${node.stable_node_id}|${node.dns_name}|${node.online}`);
  // This node rides the transport that served the page, like the Nodes page does.
  const host = $derived.by(() => {
    void nodeKey;
    return untrack(() => (node.self ? undefined : nodeHost(node)));
  });
  const nodeName = $derived(machineName(node.dns_name) || node.hostname);
  // A remote node with no address would fall back to this node, so it is shown but never asked.
  const reachable = $derived(node.self || !!node.dns_name);

  const changes = $derived(saved ? changeCount(saved, urlDraft, rows) : 0);
  const needValueAgain = $derived(saved ? rowsNeedingValueAgain(saved, urlDraft, rows) : []);
  const incomplete = $derived(hasIncompleteNewRow(rows));
  const canSave = $derived(changes > 0 && !saving && needValueAgain.length === 0 && !incomplete);
  const draft = $derived(JSON.stringify([savedVersion, urlDraft.trim(), headerEdits(rows)]));
  const shownTest = $derived(testResult?.draft === draft ? testResult.result : null);
  // An older node tests its saved URL whatever the form says, so an edited URL must be saved first.
  const canTest = $derived(
    urlDraft.trim() !== '' &&
      !testing &&
      needValueAgain.length === 0 &&
      !incomplete &&
      (headersSupported || urlDraft.trim() === saved?.url),
  );

  // Retried until the first read works, then left alone so a later answer never wipes out your typing.
  $effect(() => {
    void nodeKey;
    const online = untrack(() => node.online && reachable);
    const target = host;
    if (!online) return;
    const ac = new AbortController();
    const stop = usePolling(
      async () => {
        if (saved !== null) return;
        try {
          showSaved(await api.llmProxy(target, ac.signal));
          loadError = '';
        } catch (e) {
          if ((e as Error)?.name !== 'AbortError') {
            loadError = e instanceof Error ? e.message : String(e);
          }
        }
      },
      () => preferences.pollIntervalMs,
    );
    return () => {
      ac.abort();
      stop();
    };
  });

  function showSaved(proxy: LLMProxy) {
    headersSupported = proxy.headers !== undefined;
    saved = {
      url: proxy.url,
      headers: proxy.headers ?? [],
    };
    savedVersion++;
    resetForm(saved);
  }

  function resetForm(proxy: Required<LLMProxy>) {
    urlDraft = proxy.url;
    rows = rowsFromSaved(proxy);
    nextRowId = rows.length;
    refusedHeader = null;
    formError = '';
  }

  function discard() {
    if (saved) resetForm(saved);
    testResult = null;
  }

  function addHeader() {
    rows.push({
      id: nextRowId++,
      name: '',
      value: '',
      saved: false,
    });
  }

  function removeHeader(id: number) {
    rows = rows.filter((row) => row.id !== id);
  }

  function rowError(row: HeaderRow): string {
    if (refusedHeader && sameHeaderName(row.name, refusedHeader.name)) {
      return refusedHeader.message;
    }
    if (needValueAgain.some((needing) => needing.id === row.id)) {
      return newHostMessage;
    }
    return '';
  }

  // A refused header is shown on its row. Anything else goes under the buttons.
  function showError(e: unknown) {
    if (e instanceof ApiError && e.header) {
      const header = e.header;
      if (rows.some((row) => sameHeaderName(row.name, header))) {
        refusedHeader = {
          name: header,
          message: e.message,
        };
        return;
      }
    }
    formError = e instanceof Error ? e.message : String(e);
  }

  async function testProxy() {
    testing = true;
    testResult = null;
    formError = '';
    refusedHeader = null;
    const tested = draft;
    try {
      testResult = {
        draft: tested,
        result: await api.testLLMProxy(urlDraft.trim(), headerEdits(rows), host),
      };
    } catch (e) {
      if (tested === draft) showError(e);
    } finally {
      testing = false;
    }
  }

  async function saveProxy() {
    saving = true;
    formError = '';
    refusedHeader = null;
    try {
      showSaved(await api.saveLLMProxy(urlDraft.trim(), headerEdits(rows), host));
      showSavedCheck = true;
      setTimeout(() => {
        showSavedCheck = false;
      }, 1500);
    } catch (e) {
      showError(e);
    } finally {
      saving = false;
    }
  }
</script>

<div
  class="grid gap-2 border-t border-border pt-3 sm:grid-cols-[14rem_minmax(0,1fr)] sm:items-start sm:gap-4"
>
  <div class="min-w-0 sm:pt-6">
    <div class="truncate text-sm font-medium" title={nodeName}>{nodeName}</div>
    {#if node.self}<div class="text-xs text-muted-foreground">This machine</div>{/if}
  </div>
  {#if !node.online || !reachable}
    <p class="text-xs text-muted-foreground sm:pt-6">
      {node.online ? "Can't reach this node" : 'Offline'}
    </p>
  {:else if saved === null}
    <p class="text-xs sm:pt-6" class:text-destructive={!!loadError}>{loadError || 'Loading…'}</p>
  {:else}
    <div class="flex min-w-0 flex-col gap-3">
      <div class="flex flex-col gap-1.5">
        <div class="text-xs font-medium text-muted-foreground">URL</div>
        <input
          bind:value={urlDraft}
          disabled={saving}
          placeholder="http://100.75.35.244:9001"
          autocomplete="off"
          spellcheck="false"
          class="h-9 min-w-0 rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
          aria-label={`LLM proxy URL for ${nodeName}`}
        />
      </div>

      <div class="flex flex-col gap-1.5">
        <div class="text-xs font-medium text-muted-foreground">Headers</div>
        {#if !headersSupported}
          <p class="text-xs text-muted-foreground">
            This node runs an older deepcove. Update it to add headers.
          </p>
        {:else if rows.length === 0}
          <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <p class="text-xs text-muted-foreground">
              None yet. Add one if your proxy needs a token or other header on every request.
            </p>
            <Button size="sm" variant="outline" onclick={addHeader} disabled={saving}>
              + Add header
            </Button>
          </div>
        {:else}
          <div class="overflow-hidden rounded-lg border border-border">
            {#each rows as row (row.id)}
              {@const error = rowError(row)}
              <div
                class={[
                  'flex flex-col gap-1.5 px-3 py-2 not-first:border-t not-first:border-border',
                  (!row.saved || row.value !== '') && 'bg-muted/40',
                ]}
              >
                <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
                  {#if row.saved}
                    <div class="flex min-w-0 items-center gap-2 sm:w-[200px] sm:flex-none">
                      <span class="truncate font-mono text-[13px]" title={row.name}>
                        {row.name}
                      </span>
                      {#if row.value !== ''}
                        <span
                          class="rounded-full border border-primary px-1.5 text-[11px] font-medium text-primary"
                        >
                          Changed
                        </span>
                      {/if}
                    </div>
                  {:else}
                    <input
                      bind:value={row.name}
                      disabled={saving}
                      placeholder="Header name"
                      autocomplete="off"
                      spellcheck="false"
                      class={[
                        'h-9 min-w-0 rounded-md border bg-transparent px-2.5 font-mono text-[13px] shadow-xs focus:outline-none focus:ring-1 focus:ring-ring sm:h-8 sm:w-[200px] sm:flex-none',
                        error ? 'border-destructive' : 'border-input',
                      ]}
                      aria-label="Header name"
                    />
                  {/if}
                  <div class="flex min-w-0 flex-1 items-center gap-1">
                    <input
                      type="password"
                      bind:value={row.value}
                      disabled={saving}
                      placeholder={row.saved ? 'Saved, value hidden' : 'Value'}
                      autocomplete="off"
                      class={[
                        'h-9 min-w-0 flex-1 rounded-md border bg-transparent px-2.5 text-[13px] shadow-xs focus:outline-none focus:ring-1 focus:ring-ring sm:h-8',
                        error && row.saved ? 'border-destructive' : 'border-input',
                      ]}
                      aria-label={`Value of ${row.name || 'the new header'}`}
                    />
                    <Button
                      size="sm"
                      variant="ghost"
                      class="text-destructive"
                      disabled={saving}
                      onclick={() => removeHeader(row.id)}
                      aria-label={`Remove ${row.name || 'the new header'}`}
                    >
                      Remove
                    </Button>
                  </div>
                </div>
                {#if error}
                  <p role="alert" class="text-xs text-destructive">{error}</p>
                {/if}
              </div>
            {/each}
          </div>
          <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <p class="text-xs text-muted-foreground">
              Sent with every request to the proxy. Values stay hidden once saved.
            </p>
            <Button size="sm" variant="outline" onclick={addHeader} disabled={saving}>
              + Add header
            </Button>
          </div>
        {/if}
      </div>

      <div
        class="flex flex-col gap-2 border-t border-border pt-2.5 sm:flex-row sm:items-center sm:justify-between"
      >
        <span class="text-xs text-muted-foreground">
          {#if incomplete}
            Fill in or remove the new header.
          {:else if changes > 0}
            {changes} unsaved {changes === 1 ? 'change' : 'changes'}
          {/if}
        </span>
        <div class="flex justify-end gap-2">
          {#if changes > 0}
            <Button size="sm" variant="ghost" onclick={discard} disabled={saving}>Discard</Button>
          {/if}
          <Button size="sm" variant="ghost" onclick={testProxy} disabled={!canTest}>
            {#if testing}
              <Loader2 class="size-4 animate-spin" />
            {/if}
            Test
          </Button>
          <Button size="sm" onclick={saveProxy} disabled={!canSave}>
            {#if saving}
              <Loader2 class="size-4 animate-spin" />
            {:else if showSavedCheck}
              <Check class="size-4" />
            {/if}
            Save
          </Button>
        </div>
      </div>
      {#if formError}
        <p role="alert" class="text-xs text-destructive">{formError}</p>
      {/if}
      {#if shownTest}
        <div class="flex items-start gap-1.5 text-xs text-muted-foreground sm:justify-end">
          {#if shownTest.verdict === 'reached'}
            <CircleCheck class="mt-px size-3.5 shrink-0 text-success" aria-label="Reached" />
          {:else if shownTest.verdict === 'rejected'}
            <TriangleAlert
              class="mt-px size-3.5 shrink-0 text-warning"
              aria-label="Reached, but the headers were probably refused"
            />
          {:else if shownTest.verdict === 'unreachable'}
            <CircleX class="mt-px size-3.5 shrink-0 text-destructive" aria-label="Not reached" />
          {/if}
          <p>
            {shownTest.result}
            {#if shownTest.verdict === 'rejected'}
              <br />
              The proxy probably refused the headers.
            {/if}
          </p>
        </div>
      {/if}
    </div>
  {/if}
</div>
