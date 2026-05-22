<script lang="ts">
  import {
    Sheet,
    SheetContent,
    SheetDescription,
    SheetHeader,
    SheetTitle,
  } from '$lib/components/ui/sheet';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { api, ApiError } from '$lib/api/client';
  import type { MountRequest, Session } from '$lib/api/types';
  import Plus from '@lucide/svelte/icons/plus';
  import X from '@lucide/svelte/icons/x';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';

  // Controlled-from-parent via bind:open. session is the current
  // session (passed in so we can seed the mount rows from
  // session.mounts) and bind:session lets us push the updated row
  // back after a successful PUT — keeps the parent's UI in sync
  // without an extra fetch.
  let {
    open = $bindable(false),
    session = $bindable<Session | null>(null),
  }: {
    open?: boolean;
    session?: Session | null;
  } = $props();

  // FormMount mirrors NewSessionSheet's local shape — boolean for
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

  function addMount() {
    mounts = [
      ...mounts,
      { host_path: '', container_path: '/deepcove', read_only: false },
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
      // Drop incomplete rows — same rule as NewSessionSheet.
      .filter((m) => m.host_path && m.container_path)
      .map((m) => ({
        host_path: m.host_path,
        container_path: m.container_path,
        mode: m.read_only ? 'ro' : 'rw',
      }));

    try {
      const updated = await api.editMounts(session.id, payload);
      // Push the updated session back to the parent; their preview
      // / status row / tabs reflect the new container_id immediately.
      session = updated;
      open = false;
    } catch (e) {
      submitError =
        e instanceof ApiError
          ? e.message
          : e instanceof Error
            ? e.message
            : String(e);
    } finally {
      submitting = false;
    }
  }
</script>

<Sheet bind:open>
  <SheetContent side="right" class="sm:max-w-md">
    <SheetHeader>
      <SheetTitle>Edit mounts</SheetTitle>
      <SheetDescription>
        Add, remove, or change the host paths exposed to this session.
      </SheetDescription>
    </SheetHeader>

    <div class="mt-6 flex flex-col gap-5 overflow-y-auto px-4 pb-4">
      <!-- Recreate warning. Saving destroys the current container
           and creates a new one — the agent's conversation history
           (under /root) and the scratch /workspace survive (both
           are persistent across recreates by design), but anything
           the user installed in the writable layer is gone. -->
      <div
        class="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-sm"
        role="alert"
      >
        <TriangleAlert class="size-4 shrink-0 translate-y-0.5 text-warning" />
        <div class="space-y-1">
          <div class="font-medium">Saving recreates the container.</div>
          <p class="text-xs text-muted-foreground">
            <code>/root</code> (agent state, shell history) and
            <code>/workspace</code> are preserved. Anything else inside the
            container — installed packages, files outside those paths — will
            be lost.
          </p>
        </div>
      </div>

      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <Label>Mounts</Label>
          <Button size="sm" variant="ghost" onclick={addMount}>
            <Plus class="size-4" />
            Add mount
          </Button>
        </div>
        {#if mounts.length === 0}
          <p class="text-xs text-muted-foreground">
            No mounts. The session keeps its scratch <code>/workspace</code> and
            agent-home <code>/root</code> regardless.
          </p>
        {/if}
        {#each mounts as m, i (i)}
          <div class="space-y-2 rounded-md border border-border p-2.5">
            <div class="space-y-2">
              <div class="space-y-1">
                <Label class="text-xs" for="edit-mount-host-{i}">Host path</Label>
                <Input
                  id="edit-mount-host-{i}"
                  bind:value={m.host_path}
                  placeholder="/Users/you/code/myrepo"
                />
              </div>
              <div class="space-y-1">
                <Label class="text-xs" for="edit-mount-container-{i}">Container path</Label>
                <Input
                  id="edit-mount-container-{i}"
                  bind:value={m.container_path}
                  placeholder="/deepcove"
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
                aria-label="Remove mount"
              >
                <X class="size-4" />
              </Button>
            </div>
          </div>
        {/each}
      </div>

      {#if submitError}
        <div class="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {submitError}
        </div>
      {/if}

      <div class="flex items-center justify-end gap-2">
        <Button variant="ghost" onclick={() => (open = false)} disabled={submitting}>
          Cancel
        </Button>
        <Button onclick={submit} disabled={submitting || !session}>
          {submitting ? 'Recreating…' : 'Save and recreate'}
        </Button>
      </div>
    </div>
  </SheetContent>
</Sheet>
