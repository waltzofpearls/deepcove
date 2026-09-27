<script lang="ts">
  // The folder rows for one session's machine, with the warning and the button that commits them.
  // Its own component so the session settings dialog and the Runtimes page share one editor.
  //
  // host is the node owning the session, set for peer-hosted sessions so the save and the file browsing
  // target that node. Omit for a local session.
  import { untrack } from 'svelte';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { api, ApiError } from '$lib/api/client';
  import HostPathField from './HostPathField.svelte';
  import type { MountRequest, Session, SessionHost } from '$lib/api/types';
  import Plus from '@lucide/svelte/icons/plus';
  import X from '@lucide/svelte/icons/x';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

  let {
    session = $bindable<Session | null>(null),
    host,
    onsaved,
  }: {
    session?: Session | null;
    host?: SessionHost;
    onsaved?: () => void;
  } = $props();

  // FormMount mirrors NewSessionDialog's local shape, a boolean for RO so the checkbox binds directly.
  // Converted to MountRequest at submit time.
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

  // Seeded on the session's id, not on the session object: it churns on every list refresh, which would
  // throw away an edit in progress. A new sandbox id after a save is the same session, so that is safe.
  const sessionId = $derived(session?.id ?? '');
  $effect(() => {
    sessionId;
    // untrack, or reading session.mounts here subscribes to the session prop and the effect re-runs on
    // every refresh, which is the churn the id was meant to avoid.
    untrack(() => {
      mounts = (session?.mounts ?? []).map((m) => ({
        host_path: m.host_path,
        container_path: m.container_path,
        read_only: m.mode === 'ro',
      }));
      submitError = null;
    });
  });

  // Resolve the host home dir once, so new rows pre-fill at $HOME/.
  $effect(() => {
    if (homeDir) return;
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

  function payload(): MountRequest[] {
    return (
      mounts
        // Drop incomplete rows, same rule as NewSessionDialog.
        .filter((m) => m.host_path && m.container_path)
        .map((m) => ({
          host_path: m.host_path,
          container_path: m.container_path,
          mode: m.read_only ? 'ro' : 'rw',
        }))
    );
  }

  // Saving destroys the machine and builds a new one, so the button stays inert until something differs.
  const dirty = $derived.by(() => {
    const next = payload();
    const now = session?.mounts ?? [];
    if (next.length !== now.length) return true;
    return next.some(
      (m, i) =>
        m.host_path !== now[i].host_path ||
        m.container_path !== now[i].container_path ||
        (m.mode ?? 'rw') !== (now[i].mode ?? 'rw'),
    );
  });

  async function submit() {
    if (submitting || !session) return;
    submitting = true;
    submitError = null;
    try {
      // Push the updated session back to the parent so its preview, status row and tabs pick up the new
      // sandbox id without another fetch.
      session = await api.editMounts(session.id, payload(), host);
      onsaved?.();
    } catch (e) {
      submitError = e instanceof ApiError ? e.message : e instanceof Error ? e.message : String(e);
    } finally {
      submitting = false;
    }
  }
</script>

<div class="flex flex-col gap-5">
  <!-- Saving destroys the machine and creates a new one. The agent's /root home (agent state, shell
       history) survives; the writable layer does not, including an unmounted /workspace's contents. A
       mounted folder is host-owned and unaffected. -->
  <div
    class="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-sm"
    role="alert"
  >
    <TriangleAlert class="size-4 shrink-0 translate-y-0.5 text-warning" />
    <div class="space-y-1">
      <div class="font-medium">Saving recreates the machine.</div>
      <p class="text-xs text-muted-foreground">
        <code>/root</code>
        (agent state, shell history) and any mounted folder are preserved. Everything else inside it (installed
        packages, and an unmounted
        <code>/workspace</code>
        's contents) will be lost.
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
        No folders. The agent runs in an empty <code>/workspace</code>
        , with the shared
        <code>/root</code>
        home regardless.
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
          <label class="flex cursor-pointer items-center gap-1.5 text-xs select-none">
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

  <div class="flex items-center justify-end">
    <Button size="sm" onclick={submit} disabled={submitting || !session || !dirty}>
      {submitting ? 'Recreating…' : 'Save and recreate'}
    </Button>
  </div>
</div>
