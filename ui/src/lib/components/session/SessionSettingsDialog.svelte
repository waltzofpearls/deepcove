<script lang="ts">
  // One session: what it is, and the two things you can change about it. Switches apply as you flip
  // them; folders recreate the machine, so they commit on their own button.
  import { Dialog, DialogContent, DialogHeader, DialogTitle } from '$lib/components/ui/dialog';
  import { untrack } from 'svelte';
  import { Switch } from '$lib/components/ui/switch';
  import { Button } from '$lib/components/ui/button';
  import { api } from '$lib/api/client';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { formatResources } from '$lib/util/resources';
  import { runtimeLabel } from '$lib/util/runtime';
  import { shortId } from '$lib/format';
  import SharedFolderEditor from './SharedFolderEditor.svelte';
  import WorkspaceVMDialog from './WorkspaceVMDialog.svelte';
  import type { Session, SessionHost, WorkspaceVM } from '$lib/api/types';

  let {
    open = $bindable(false),
    session = $bindable<Session>(),
    host,
  }: {
    open?: boolean;
    session: Session;
    host?: SessionHost;
  } = $props();

  let machineOpen = $state(false);

  // The machine's folder and size live on the workspace record, not on the session.
  let workspaceVM = $state<WorkspaceVM | null>(null);
  $effect(() => {
    if (!open || !session.on_workspace_vm) return;
    const ac = new AbortController();
    api
      .workspaceVM(host, ac.signal)
      .then((vm) => (workspaceVM = vm))
      .catch(() => {});
    return () => ac.abort();
  });

  // Kept locally so the row follows the click without waiting for the session list to refresh. Seeded
  // on open, and keyed on the id: the dialog outlives each use, and the session object itself churns.
  let useProxy = $state(false);
  let saving = $state(false);
  let saveError = $state<string | null>(null);
  const sessionId = $derived(session.id);
  $effect(() => {
    if (!open) return;
    sessionId;
    untrack(() => {
      useProxy = session.use_llm_proxy ?? false;
      saveError = null;
    });
  });

  // The same question the new session dialog asks, so it names the proxy here too.
  let llmProxyUrl = $state('');
  $effect(() => {
    if (!open) return;
    const ac = new AbortController();
    api
      .llmProxy(host, ac.signal)
      .then((p) => (llmProxyUrl = p.url ?? ''))
      .catch(() => {});
    return () => ac.abort();
  });

  async function toggleProxy(next: boolean) {
    saving = true;
    saveError = null;
    try {
      const updated = await api.setSessionUseLLMProxy(session.id, next, host);
      session = updated;
      useProxy = updated.use_llm_proxy ?? next;
      void sessions.refresh();
    } catch (e) {
      saveError = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
  }

  const machine = $derived(session.on_workspace_vm ? 'Workspace VM' : 'Session VM');
  const size = $derived(
    session.on_workspace_vm
      ? formatResources(workspaceVM?.resources) || 'runtime defaults'
      : formatResources(session.resources) || 'runtime defaults',
  );
</script>

<Dialog bind:open>
  <DialogContent class="max-h-[90vh] w-[calc(100%-2rem)] overflow-y-auto sm:max-w-2xl">
    <DialogHeader>
      <DialogTitle class="font-mono">{session.name}</DialogTitle>
    </DialogHeader>

    <dl class="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-2 text-sm">
      <dt class="text-muted-foreground">Agent</dt>
      <dd>{session.agent}</dd>

      <dt class="text-muted-foreground">Status</dt>
      <dd>{session.status}</dd>

      <dt class="text-muted-foreground">Machine</dt>
      <dd>{machine} · {runtimeLabel(session.runtime)}</dd>

      <dt class="text-muted-foreground">Folder</dt>
      <dd class="font-mono text-xs break-all">
        {#if session.work_dir}
          {session.work_dir}
          {#if workspaceVM}
            <span class="text-muted-foreground">(in {workspaceVM.host_root})</span>
          {/if}
        {:else if session.mounts?.length}
          {session.mounts.map((m) => `${m.host_path} → ${m.container_path}`).join(', ')}
        {:else}
          /workspace <span class="text-muted-foreground">(nothing mounted)</span>
        {/if}
      </dd>

      <dt class="text-muted-foreground">Size</dt>
      <dd>{size}</dd>

      <dt class="text-muted-foreground">Image</dt>
      <dd class="font-mono text-xs break-all">{session.image}</dd>

      <dt class="text-muted-foreground">Sandbox</dt>
      <dd class="font-mono text-xs">{shortId(session.sandbox_id ?? '', 24) || 'none yet'}</dd>

      <dt class="text-muted-foreground">Session id</dt>
      <dd class="font-mono text-xs break-all">{session.id}</dd>
    </dl>

    <div class="flex items-start justify-between gap-4 border-t border-border pt-4">
      <button
        type="button"
        disabled={saving}
        onclick={() => toggleProxy(!useProxy)}
        class="space-y-1 text-left"
      >
        <span class="block text-sm font-medium">Use the LLM proxy</span>
        <span class="block text-xs leading-relaxed text-muted-foreground">
          {#if llmProxyUrl}<span class="font-mono">{llmProxyUrl}</span>{:else}The proxy{/if} supplies
          the key. Off uses this session's own login. Changing this restarts the agent.
        </span>
        {#if saveError}<span class="block text-xs text-destructive">{saveError}</span>{/if}
      </button>
      <Switch
        checked={useProxy}
        disabled={saving}
        onCheckedChange={toggleProxy}
        class="mt-0.5 shrink-0"
        aria-label="Use the LLM proxy"
      />
    </div>

    <div class="border-t border-border pt-4">
      {#if session.on_workspace_vm}
        <div class="flex items-start justify-between gap-4">
          <div class="space-y-1">
            <p class="text-sm font-medium">This is the workspace VM</p>
            <p class="text-xs leading-relaxed text-muted-foreground">
              Its folder belongs to the machine, so changing it changes every session on it.
            </p>
          </div>
          <!-- Disabled until the record arrives: the dialog with no record is the CREATE form, and
               saving that would overwrite this machine's folder and size with blanks. -->
          <Button
            size="sm"
            variant="secondary"
            class="shrink-0"
            disabled={!workspaceVM}
            onclick={() => (machineOpen = true)}
          >
            Edit the machine
          </Button>
        </div>
      {:else}
        <SharedFolderEditor bind:session {host} />
      {/if}
    </div>
  </DialogContent>
</Dialog>

{#if workspaceVM}
  <WorkspaceVMDialog bind:open={machineOpen} vm={workspaceVM} />
{/if}
