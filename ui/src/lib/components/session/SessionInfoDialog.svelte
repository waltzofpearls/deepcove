<script lang="ts">
  // Everything about one session that does not fit on its card: which machine it is on, the folder it
  // works in, its size and image.
  import { Dialog, DialogContent, DialogHeader, DialogTitle } from '$lib/components/ui/dialog';
  import { api } from '$lib/api/client';
  import { formatResources } from '$lib/util/resources';
  import { runtimeLabel } from '$lib/util/runtime';
  import { shortId } from '$lib/format';
  import type { Session, WorkspaceVM } from '$lib/api/types';

  let { open = $bindable(false), session }: { open?: boolean; session: Session } = $props();

  // The machine's folder and size live on the workspace record, not on the session.
  let workspaceVM = $state<WorkspaceVM | null>(null);
  $effect(() => {
    if (!open || !session.on_workspace_vm) return;
    const ac = new AbortController();
    api
      .workspaceVM(session.host, ac.signal)
      .then((vm) => (workspaceVM = vm))
      .catch(() => {});
    return () => ac.abort();
  });

  const machine = $derived(session.on_workspace_vm ? 'Workspace VM (shared)' : 'Its own machine');
  const size = $derived(
    session.on_workspace_vm
      ? formatResources(workspaceVM?.resources) || 'runtime defaults'
      : formatResources(session.resources) || 'runtime defaults',
  );
</script>

<Dialog bind:open>
  <DialogContent class="sm:max-w-lg">
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
  </DialogContent>
</Dialog>
