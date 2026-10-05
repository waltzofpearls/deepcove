<script lang="ts">
  import type { SessionHost } from '$lib/api/types';
  import { untrack } from 'svelte';
  // Create or edit the workspace VM. A dialog rather than a form on the page, because the Nodes page
  // lists several machines and a form under them reads as belonging to the last one.
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
  } from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import WorkspaceVMFields from './WorkspaceVMFields.svelte';
  import { Label } from '$lib/components/ui/label';
  import { api } from '$lib/api/client';
  import { looksLikePath, withoutTrailingSlash } from '$lib/util/path';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { DEFAULT_VM_SIZE, describeVMSize, formatRunningSize } from '$lib/util/vmSize';
  import { folderAwaitingRestart, sizeAwaitingRestart } from '$lib/util/pendingRestart';
  import type { SandboxView, WorkspaceVM } from '$lib/api/types';

  let {
    open = $bindable(false),
    vm = null,
    onsaved,
    host,
  }: {
    open?: boolean;
    vm?: WorkspaceVM | null;
    host?: SessionHost;
    onsaved?: () => void;
  } = $props();

  let hostRoot = $state('');
  let guestRoot = $state('/workspace');
  let size = $state(DEFAULT_VM_SIZE);
  let setupScript = $state('');
  let error = $state<string | null>(null);
  let saving = $state(false);

  // The dialog's own element, focused on open so the folder field does not take it.
  let contentRef = $state<HTMLElement | null>(null);

  // The record as last saved, kept here so a save without a restart shows what is waiting for one.
  let savedVM = $state<WorkspaceVM | null>(null);
  // The VM as it runs right now. Null when it is not running.
  let runningSandbox = $state<SandboxView | null>(null);

  async function loadRunningSandbox() {
    try {
      const views = await api.runtimes(host);
      runningSandbox =
        views.runtimes
          .flatMap((view) => view.sandboxes)
          .find((sandbox) => sandbox.is_workspace_vm && sandbox.state === 'running') ?? null;
    } catch {
      runningSandbox = null;
    }
  }

  const sessionsOnVM = $derived(runningSandbox?.sessions.length ?? 0);
  const sessionsToInterrupt = $derived(
    sessionsOnVM === 1 ? 'the 1 session' : `all ${sessionsOnVM} sessions`,
  );
  const changed = $derived(
    Boolean(savedVM) &&
      (withoutTrailingSlash(hostRoot.trim()) !== savedVM?.host_root ||
        withoutTrailingSlash(guestRoot.trim() || '/workspace') !== savedVM?.guest_root ||
        size !== (savedVM?.resources?.size ?? '') ||
        setupScript.trim() !== (savedVM?.setup_script ?? '')),
  );
  // A change only reaches a running VM when it restarts, so then you pick when.
  const offerRestart = $derived(changed && Boolean(runningSandbox));

  const sizeWaiting = $derived(
    sizeAwaitingRestart(
      runningSandbox?.resources,
      savedVM?.resources,
      preflight.runtimes?.workspace_vm_sizes,
    ),
  );
  const folderWaiting = $derived(
    savedVM !== null &&
      folderAwaitingRestart(runningSandbox?.mounts, savedVM.host_root, savedVM.guest_root),
  );

  // Seeded on open rather than on mount: the dialog outlives each use, so a stale form would show the
  // previous machine. Editing starts from what the machine is; creating starts from the default size.
  //
  // Only `open` is tracked. Reading `vm` here would re-seed on every poll that hands us a new object,
  // wiping what the user typed.
  $effect(() => {
    if (!open) return;
    untrack(() => {
      hostRoot = vm?.host_root ?? '';
      guestRoot = vm?.guest_root ?? '/workspace';
      size = vm?.resources?.size || DEFAULT_VM_SIZE;
      setupScript = vm?.setup_script ?? '';
      error = null;
      savedVM = vm;
    });
    untrack(() => void loadRunningSandbox());
  });

  let restarting = $state(false);
  const busy = $derived(saving || restarting);

  async function saveRecord(): Promise<WorkspaceVM | null> {
    try {
      return await api.saveWorkspaceVM(
        {
          host_root: hostRoot.trim(),
          guest_root: guestRoot.trim() || undefined,
          setup_script: setupScript.trim() || undefined,
          resources: { size },
        },
        host,
      );
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      return null;
    }
  }

  // Not running, or nothing to restart for: the change is saved and that is all there is.
  async function save() {
    if (saving) return;
    saving = true;
    error = null;
    const saved = await saveRecord();
    saving = false;
    if (!saved) return;
    open = false;
    onsaved?.();
  }

  // Kept open, so the warning and its Restart now button show what is still waiting.
  async function saveForLater() {
    if (saving) return;
    saving = true;
    error = null;
    const saved = await saveRecord();
    saving = false;
    if (!saved) return;
    savedVM = saved;
    onsaved?.();
    await loadRunningSandbox();
  }

  async function restartVM() {
    if (!savedVM || !runningSandbox) return;
    restarting = true;
    error = null;
    try {
      await api.restartSandbox(savedVM.runtime, runningSandbox.sandbox_id, host);
      await loadRunningSandbox();
      onsaved?.();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      restarting = false;
    }
  }

  async function saveAndRestart() {
    if (saving) return;
    saving = true;
    error = null;
    const saved = await saveRecord();
    if (saved) {
      savedVM = saved;
      onsaved?.();
    }
    const sandboxID = runningSandbox?.sandbox_id;
    if (saved && sandboxID) {
      restarting = true;
      try {
        await api.restartSandbox(saved.runtime, sandboxID, host);
        await loadRunningSandbox();
      } catch (e) {
        error = `Saved, but the restart failed: ${e instanceof Error ? e.message : String(e)}`;
      }
      restarting = false;
    }
    saving = false;
    if (!saved || error) return;
    open = false;
  }
</script>

<Dialog bind:open>
  <DialogContent
    class="max-h-[90vh] w-[calc(100%-2rem)] overflow-y-auto sm:max-w-2xl"
    bind:ref={contentRef}
    tabindex={-1}
    onOpenAutoFocus={(e) => {
      // Focus would land on the folder field, whose suggestions then cover the dialog.
      e.preventDefault();
      contentRef?.focus();
    }}
  >
    <DialogHeader>
      <DialogTitle>{vm ? 'Edit the workspace VM' : 'Create a workspace VM'}</DialogTitle>
      <DialogDescription>
        One VM your sessions share, each working in its own folder inside the folder you pick.
      </DialogDescription>
    </DialogHeader>

    <div class="space-y-4">
      <WorkspaceVMFields bind:hostRoot bind:guestRoot bind:size {host} />
      <div class="space-y-2">
        <Label for="workspace-setup">Setup script</Label>
        <textarea
          id="workspace-setup"
          bind:value={setupScript}
          rows="3"
          spellcheck="false"
          placeholder="Leave blank for the agent's default"
          class="w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs focus:outline-none focus:ring-1 focus:ring-ring"></textarea>
        <p class="text-xs text-muted-foreground">
          Runs once when the machine is built, and installs the agent. Sessions on it have none of
          their own.
        </p>
      </div>

      {#if error}
        <p class="text-sm text-destructive">{error}</p>
      {/if}
      {#if (sizeWaiting || folderWaiting) && runningSandbox && savedVM}
        <div
          class="flex items-start justify-between gap-4 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-sm"
          role="status"
        >
          <div class="space-y-1">
            <div class="font-medium">Saved changes are not in use yet.</div>
            <p class="text-xs text-muted-foreground">
              {#if sizeWaiting}
                The VM still runs with {formatRunningSize(runningSandbox.resources)}, not
                {describeVMSize(savedVM.resources, preflight.runtimes?.workspace_vm_sizes)}.
              {/if}
              {#if folderWaiting}
                It still shares {runningSandbox.mounts?.[0]?.host_path}, not {savedVM.host_root}.
              {/if}
              Restarting applies them and interrupts {sessionsToInterrupt} on this VM.
            </p>
          </div>
          <Button size="sm" class="shrink-0" disabled={busy} onclick={restartVM}>
            {restarting ? 'Restarting…' : 'Restart now'}
          </Button>
        </div>
      {/if}
      {#if offerRestart}
        <p class="text-xs text-muted-foreground">
          A change takes effect when the VM restarts. Restarting interrupts {sessionsToInterrupt} on this
          VM.
        </p>
      {/if}
      <div class="flex items-center gap-2">
        {#if offerRestart}
          <Button size="sm" onclick={saveAndRestart} disabled={busy || !looksLikePath(hostRoot)}>
            {restarting ? 'Restarting…' : saving ? 'Saving…' : 'Save and restart'}
          </Button>
          <Button
            size="sm"
            variant="secondary"
            onclick={saveForLater}
            disabled={busy || !looksLikePath(hostRoot)}
          >
            Save, restart later
          </Button>
        {:else}
          <Button size="sm" onclick={save} disabled={busy || !looksLikePath(hostRoot)}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
        {/if}
        <Button size="sm" variant="ghost" onclick={() => (open = false)}>Cancel</Button>
      </div>
    </div>
  </DialogContent>
</Dialog>
