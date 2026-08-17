<script lang="ts">
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
  } from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { api, ApiError } from '$lib/api/client';
  import HostPathField from './HostPathField.svelte';
  import type { MountRequest, Session, SessionHost } from '$lib/api/types';
  import Plus from '@lucide/svelte/icons/plus';
  import X from '@lucide/svelte/icons/x';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

  // Controlled-from-parent via bind:open. session is the current
  // session (passed in so we can seed the mount rows from
  // session.mounts) and bind:session lets us push the updated row
  // back after a successful PUT, keeping the parent's UI in sync
  // without an extra fetch.
  // host (optional) is the node owning the session, set for peer-hosted sessions so the mount edit and
  // its filesystem browsing target that node over WASM. Omit for a local session.
  let {
    open = $bindable(false),
    session = $bindable<Session | null>(null),
    host,
  }: {
    open?: boolean;
    session?: Session | null;
    host?: SessionHost;
  } = $props();

  // FormMount mirrors NewSessionDialog's local shape, a boolean for
  // RO so checkbox binding is straightforward. Converted to
  // MountRequest at submit time.
  type FormMount = {
    host_path: string;
    container_path: string;
    read_only: boolean;
  };

  let mounts = $state<FormMount[]>([]);
  let submitting = $state(false);
  let submitError = $state<string | null>(null);
  // Host home dir, where the host-path picker starts; used to pre-fill new rows.
  let homeDir = $state('');

  // Hydrate the form whenever the sheet opens with a session. We seed
  // on open (not on session change) so reopening after a successful
  // submit picks up the freshly-updated session, and re-editing
  // mid-flight without closing doesn't reset the user's edits.
  $effect(() => {
    if (!open || !session) return;
    mounts = (session.mounts ?? []).map((m) => ({
      host_path: m.host_path,
      container_path: m.container_path,
      read_only: m.mode === 'ro',
    }));
    submitError = null;
  });

  // Resolve the host home dir once per open, so new rows pre-fill at $HOME/.
  $effect(() => {
    if (!open || homeDir) return;
    api
      .fsList(undefined, host)
      .then((res) => (homeDir = res.path))
      .catch(() => {});
  });

  function addMount() {
    mounts = [
      ...mounts,
      { host_path: homeDir ? homeDir + '/' : '', container_path: '/workspace', read_only: false },
    ];
  }

  function removeMount(i: number) {
    mounts = mounts.filter((_, idx) => idx !== i);
  }

  async function submit() {
    if (submitting || !session) return;
    submitting = true;
    submitError = null;

    const payload: MountRequest[] = mounts
      // Drop incomplete rows, same rule as NewSessionDialog.
      .filter((m) => m.host_path && m.container_path)
      .map((m) => ({
        host_path: m.host_path,
        container_path: m.container_path,
        mode: m.read_only ? 'ro' : 'rw',
      }));

    try {
      const updated = await api.editMounts(session.id, payload, host);
      // Push the updated session back to the parent; their preview
      // / status row / tabs reflect the new sandbox_id immediately.
      session = updated;
      open = false;
    } catch (e) {
      submitError = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
    } finally {
      submitting = false;
    }
  }
</script>

<Dialog bind:open>
  <DialogContent class="max-h-[90vh] w-[calc(100%-2rem)] max-w-2xl gap-0 p-0 sm:max-w-2xl">
    <DialogHeader class="border-b border-border px-6 py-4">
      <DialogTitle>Edit mounts</DialogTitle>
      <DialogDescription>
        Add, remove, or change the host paths exposed to this session.
      </DialogDescription>
    </DialogHeader>

    <div class="flex flex-col gap-5 overflow-y-auto px-6 pb-6 pt-4">
      <!-- Recreate warning. Saving destroys the current container and
           creates a new one. The shared /root volume (agent state, shell
           history) survives; the writable layer does not, including a
           no-mount /workspace's scratch contents. A mounted folder is
           host-owned and unaffected. -->
      <div
        class="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-sm"
        role="alert"
      >
        <TriangleAlert class="size-4 shrink-0 translate-y-0.5 text-warning" />
        <div class="space-y-1">
          <div class="font-medium">Saving recreates the container.</div>
          <p class="text-xs text-muted-foreground">
            <code>/root</code> (agent state, shell history) and any mounted folder are preserved.
            Everything else inside the container (installed packages, and an unmounted
            <code>/workspace</code>'s contents) will be lost.
          </p>
        </div>
      </div>

      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <Label>Folders</Label>
          <Button size="sm" variant="ghost" onclick={addMount}>
            <Plus class="size-4" />
            Add a folder
          </Button>
        </div>
        {#if mounts.length === 0}
          <p class="text-xs text-muted-foreground">
            No folders. The agent runs in an empty <code>/workspace</code>, with the shared
            <code>/root</code> home regardless.
          </p>
        {/if}
        {#each mounts as m, i (i)}
          <div class="space-y-2 rounded-md border border-border p-2.5">
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <div class="space-y-1">
                <Label class="text-xs" for="edit-mount-host-{i}">Host path</Label>
                <HostPathField id="edit-mount-host-{i}" bind:value={m.host_path} {host} />
              </div>
              <div class="space-y-1">
                <Label class="text-xs" for="edit-mount-container-{i}">Mounts to</Label>
                <Input
                  id="edit-mount-container-{i}"
                  bind:value={m.container_path}
                  placeholder="/workspace"
                />
              </div>
            </div>
            <div class="flex items-center justify-between">
              <label class="flex cursor-pointer select-none items-center gap-1.5 text-xs">
                <input
                  type="checkbox"
                  bind:checked={m.read_only}
                  class="size-3.5 rounded border-input"
                />
                Read-only
              </label>
              <Button
                size="sm"
                variant="ghost"
                onclick={() => removeMount(i)}
                aria-label="Remove folder"
              >
                <X class="size-4" />
              </Button>
            </div>
          </div>
        {/each}
      </div>

      {#if submitError}
        <div
          class="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          {submitError}
        </div>
      {/if}

      <div class="flex items-center justify-end gap-2">
        <Button variant="ghost" onclick={() => (open = false)} disabled={submitting}>Cancel</Button>
        <Button onclick={submit} disabled={submitting || !session}>
          {submitting ? 'Recreating…' : 'Save and recreate'}
        </Button>
      </div>
    </div>
  </DialogContent>
</Dialog>
