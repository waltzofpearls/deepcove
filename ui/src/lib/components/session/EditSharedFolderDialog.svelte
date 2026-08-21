<script lang="ts">
  // The folder editor on its own, opened from the Runtimes page where a machine has no session view to
  // open. The session view embeds SharedFolderEditor directly instead.
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
  } from '$lib/components/ui/dialog';
  import SharedFolderEditor from './SharedFolderEditor.svelte';
  import type { Session, SessionHost } from '$lib/api/types';

  let {
    open = $bindable(false),
    session = $bindable<Session | null>(null),
    host,
    onsaved,
  }: {
    open?: boolean;
    session?: Session | null;
    host?: SessionHost;
    onsaved?: () => void;
  } = $props();
</script>

<Dialog bind:open>
  <DialogContent class="max-h-[90vh] w-[calc(100%-2rem)] max-w-2xl gap-0 p-0 sm:max-w-2xl">
    <DialogHeader class="border-b border-border px-6 py-4">
      <DialogTitle>{session?.name ?? 'Edit folders'}</DialogTitle>
      <DialogDescription>
        Add, remove, or change the host paths this machine shares.
      </DialogDescription>
    </DialogHeader>

    <div class="overflow-y-auto px-6 pt-4 pb-6">
      <SharedFolderEditor
        bind:session
        {host}
        onsaved={() => {
          open = false;
          onsaved?.();
        }}
      />
    </div>
  </DialogContent>
</Dialog>
