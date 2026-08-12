<script lang="ts">
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
  import WorkspaceVMFields from './WorkspaceVMFields.svelte';
  import { Label } from '$lib/components/ui/label';
  import { api } from '$lib/api/client';
  import { looksLikePath } from '$lib/util/path';
  import { emptyResourceFields, fieldsFromDefaults, parseResources } from '$lib/util/resources';
  import type { WorkspaceVM } from '$lib/api/types';

  let {
    open = $bindable(false),
    vm = null,
    onsaved,
  }: { open?: boolean; vm?: WorkspaceVM | null; onsaved?: () => void } = $props();

  let hostRoot = $state('');
  let guestRoot = $state('/workspace');
  let fields = $state({ ...emptyResourceFields });
  let setupScript = $state('');
  let error = $state<string | null>(null);
  let saving = $state(false);

  const parsed = $derived(parseResources(fields, true));

  // Seeded on open rather than on mount: the dialog outlives each use, so a stale form would show the
  // previous machine. Editing starts from what the machine is; creating starts from the host defaults,
  // which the fields component fills in.
  $effect(() => {
    if (!open) return;
    hostRoot = vm?.host_root ?? '';
    guestRoot = vm?.guest_root ?? '/workspace';
    fields = vm ? fieldsFromDefaults(vm.resources) : { ...emptyResourceFields };
    setupScript = vm?.setup_script ?? '';
    error = null;
  });

  async function save() {
    if (saving) return;
    if (parsed.error) {
      error = parsed.error;
      return;
    }
    saving = true;
    error = null;
    try {
      await api.saveWorkspaceVM({
        host_root: hostRoot.trim(),
        guest_root: guestRoot.trim() || undefined,
        setup_script: setupScript.trim() || undefined,
        resources: parsed.resources,
      });
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
  <DialogContent class="sm:max-w-lg">
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
        <WorkspaceVMFields bind:hostRoot bind:guestRoot bind:fields />
      {/key}
      <div class="space-y-2">
        <Label for="workspace-setup">Setup script</Label>
        <textarea
          id="workspace-setup"
          bind:value={setupScript}
          rows="3"
          spellcheck="false"
          placeholder="Leave blank for the agent's default"
          class="w-full rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs focus:outline-none focus:ring-1 focus:ring-ring"
        ></textarea>
        <p class="text-xs text-muted-foreground">
          Runs once when the machine is built, and installs the agent. Sessions on it have none of
          their own.
        </p>
      </div>

      {#if error}
        <p class="text-sm text-destructive">{error}</p>
      {/if}
      {#if vm?.sandbox_id}
        <p class="text-xs text-muted-foreground">
          A new folder or size applies the next time the VM is built.
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
