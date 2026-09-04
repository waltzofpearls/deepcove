<script lang="ts">
  import type { SessionHost } from '$lib/api/types';
  import { untrack } from 'svelte';
  // Create or edit the workspace VM. A dialog rather than a form on the page, because the Runtimes page
  // lists several machines and a form under them reads as belonging to the last one.
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
  } from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
  } from '$lib/components/ui/alert-dialog';
  import WorkspaceVMFields from './WorkspaceVMFields.svelte';
  import { Label } from '$lib/components/ui/label';
  import { api } from '$lib/api/client';
  import { looksLikePath } from '$lib/util/path';
  import {
    emptyResourceFields,
    fieldsFromDefaults,
    formatResources,
    parseResources,
  } from '$lib/util/resources';
  import type { SessionResources, WorkspaceVM } from '$lib/api/types';

  // `running` is the size the VM booted with, which the caller reads off the machine itself. It differs
  // from the saved size whenever the settings changed after the last boot.
  let {
    open = $bindable(false),
    vm = null,
    running = undefined,
    onsaved,
    host,
  }: {
    open?: boolean;
    vm?: WorkspaceVM | null;
    running?: SessionResources;
    host?: SessionHost;
    onsaved?: () => void;
  } = $props();

  let hostRoot = $state('');
  let guestRoot = $state('/workspace');
  let fields = $state({ ...emptyResourceFields });
  let setupScript = $state('');
  let error = $state<string | null>(null);
  let saving = $state(false);

  // Asked before a save the running VM will not pick up until it restarts.
  let confirmOpen = $state(false);

  // The dialog's own element, focused on open so the folder field does not take it.
  let contentRef = $state<HTMLElement | null>(null);

  const parsed = $derived(parseResources(fields, true));

  const runningSize = $derived(formatResources(running));

  // True when saving this form would leave the running VM behind.
  const changesRunning = $derived(
    Boolean(vm?.sandbox_id) &&
      runningSize !== '' &&
      runningSize !== formatResources(parsed.resources),
  );

  // What the VM is running with, when that is not what is saved. Empty means there is nothing to say.
  const driftedSize = $derived(
    vm?.sandbox_id && runningSize && runningSize !== formatResources(vm.resources)
      ? runningSize
      : '',
  );

  // Seeded on open rather than on mount: the dialog outlives each use, so a stale form would show the
  // previous machine. Editing starts from what the machine is; creating starts from the host defaults,
  // which the fields component fills in.
  //
  // Only `open` is tracked. Reading `vm` here would re-seed on every poll that hands us a new object,
  // wiping what the user typed.
  $effect(() => {
    if (!open) return;
    untrack(() => {
      hostRoot = vm?.host_root ?? '';
      guestRoot = vm?.guest_root ?? '/workspace';
      fields = vm ? fieldsFromDefaults(vm.resources) : { ...emptyResourceFields };
      setupScript = vm?.setup_script ?? '';
      error = null;
      confirmOpen = false;
    });
  });

  function save() {
    if (parsed.error) {
      error = parsed.error;
      return;
    }
    if (changesRunning) {
      confirmOpen = true;
      return;
    }
    void doSave();
  }

  async function doSave() {
    if (saving) return;
    confirmOpen = false;
    saving = true;
    error = null;
    try {
      await api.saveWorkspaceVM(
        {
          host_root: hostRoot.trim(),
          guest_root: guestRoot.trim() || undefined,
          setup_script: setupScript.trim() || undefined,
          resources: parsed.resources,
        },
        host,
      );
      open = false;
      onsaved?.();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      saving = false;
    }
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
      <!-- Keyed on open so the fields remount: their "you edited the size" flag would otherwise survive
           into the next time this dialog is used. -->
      {#key open}
        <WorkspaceVMFields bind:hostRoot bind:guestRoot bind:fields creating={!vm} {host} />
      {/key}
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
      {#if driftedSize}
        <p class="text-sm text-amber-600 dark:text-amber-400">
          The VM is running with {driftedSize}, not what is saved here. Restart it from the Runtimes
          page to apply.
        </p>
      {:else if vm?.sandbox_id}
        <p class="text-xs text-muted-foreground">
          A new folder or size applies when you restart the VM.
        </p>
      {/if}
      <div class="flex items-center gap-2">
        <Button size="sm" onclick={save} disabled={saving || !looksLikePath(hostRoot)}>
          {saving ? 'Saving…' : 'Save'}
        </Button>
        <Button size="sm" variant="ghost" onclick={() => (open = false)}>Cancel</Button>
      </div>
    </div>
  </DialogContent>
</Dialog>

<AlertDialog bind:open={confirmOpen}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>Save without restarting?</AlertDialogTitle>
      <AlertDialogDescription>
        The VM is running with {runningSize}. It keeps running with that until you restart it from
        the Runtimes page.
      </AlertDialogDescription>
    </AlertDialogHeader>
    <AlertDialogFooter>
      <AlertDialogCancel>Cancel</AlertDialogCancel>
      <AlertDialogAction onclick={doSave}>Save</AlertDialogAction>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>
