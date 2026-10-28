<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { api } from '$lib/api/client';
  import { friendlyError, type FriendlyError } from '$lib/api/errors';
  import { looksLikePath } from '$lib/util/path';
  import { DEFAULT_VM_SIZE } from '$lib/util/vmSize';
  import WorkspaceVMFields from './WorkspaceVMFields.svelte';
  import type { SessionHost, WorkspaceVM, WorkspaceVMSizeChoice } from '$lib/api/types';
  import Info from '@lucide/svelte/icons/info';

  // Creates the node's workspace VM before the first session on it. Saving only records the machine.
  // It boots when that session starts.
  let {
    host,
    nodeName,
    sizes,
    onback,
    oncreated,
  }: {
    host?: SessionHost;
    nodeName: string;
    sizes?: WorkspaceVMSizeChoice[];
    onback: () => void;
    oncreated: (vm: WorkspaceVM) => void;
  } = $props();

  let hostRoot = $state('');
  let guestRoot = $state('/workspace');
  let size = $state(DEFAULT_VM_SIZE);
  let saving = $state(false);
  let error = $state<FriendlyError | null>(null);

  const ready = $derived(
    looksLikePath(hostRoot) && looksLikePath(guestRoot) && (sizes?.length ?? 0) > 0,
  );

  async function create() {
    saving = true;
    error = null;
    try {
      const vm = await api.saveWorkspaceVM(
        { host_root: hostRoot.trim(), guest_root: guestRoot.trim(), resources: { size } },
        host,
      );
      oncreated(vm);
    } catch (e) {
      error = friendlyError(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="space-y-5">
  <div class="space-y-1">
    <h2 class="text-lg font-semibold">Set up your workspace VM</h2>
    <p class="text-sm text-muted-foreground">
      You only do this once. Every session you put on it shares this VM, each in its own folder.
    </p>
  </div>

  <WorkspaceVMFields bind:hostRoot bind:guestRoot bind:size {host} {nodeName} {sizes} />

  <div
    class="flex items-center gap-2.5 rounded-md border border-border px-3 py-2.5 text-xs text-muted-foreground"
  >
    <Info class="size-4 shrink-0" />
    Next time you pick Workspace VM on this node, this step is skipped.
  </div>

  {#if error}
    <div class="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2.5 text-sm">
      <div class="font-medium text-destructive">{error.title}</div>
      {#if error.guidance}
        <div class="mt-0.5 text-xs text-foreground">{error.guidance}</div>
      {/if}
    </div>
  {/if}

  <div class="flex items-center justify-between gap-2">
    <Button variant="ghost" onclick={onback} disabled={saving}>Back</Button>
    <Button onclick={create} disabled={!ready || saving}>
      {saving ? 'Creating…' : 'Create VM and continue'}
    </Button>
  </div>
</div>
