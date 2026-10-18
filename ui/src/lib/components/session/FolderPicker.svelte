<script lang="ts">
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
  } from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { api, ApiError } from '$lib/api/client';
  import type { FsEntry, SessionHost } from '$lib/api/types';
  import Folder from '@lucide/svelte/icons/folder';
  import House from '@lucide/svelte/icons/house';
  import CornerLeftUp from '@lucide/svelte/icons/corner-left-up';

  // Browse modal for picking a host directory. value (two-way) is the
  // chosen absolute path; start seeds the initial directory (falls back
  // to value, then the home dir). Listing is scoped to $HOME server-side.
  // host (optional) browses a remote node's filesystem (a session living on a peer); omit for this one.
  let {
    open = $bindable(false),
    value = $bindable(''),
    start = '',
    host,
  }: { open?: boolean; value?: string; start?: string; host?: SessionHost } = $props();

  let path = $state('');
  let parent = $state('');
  let entries = $state<FsEntry[]>([]);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let pathEl = $state<HTMLElement | undefined>(undefined);

  async function load(p?: string) {
    loading = true;
    error = null;
    try {
      const res = await api.fsList(p, host);
      path = res.path;
      parent = res.parent;
      entries = res.entries;
    } catch (e) {
      // A folder that isn't there yet, like a new session's, opens at the nearest one that is.
      if (p && e instanceof ApiError && e.status === 404) {
        const up = p.replace(/\/+$/, '').replace(/\/[^/]*$/, '') || '/';
        if (up !== p) {
          await load(up);
          return;
        }
      }
      error = e instanceof Error ? e.message : String(e);
      entries = [];
    } finally {
      loading = false;
    }
  }

  // (Re)load whenever the modal opens, starting from start/value, else home.
  $effect(() => {
    if (!open) return;
    load(start || value || undefined);
  });

  // Keep the path box scrolled to the end so the current (deepest) folder is
  // visible rather than the /Users/... prefix.
  $effect(() => {
    if (pathEl && path) pathEl.scrollLeft = pathEl.scrollWidth;
  });

  function selectCurrent() {
    value = path;
    open = false;
  }
</script>

<Dialog bind:open>
  <DialogContent
    class="max-h-[80vh] w-[calc(100%-2rem)] max-w-lg gap-0 overflow-hidden p-0 sm:max-w-lg"
  >
    <DialogHeader class="border-b border-border px-5 py-3.5">
      <DialogTitle>Choose a folder</DialogTitle>
      <DialogDescription>Pick a directory under your home folder to share.</DialogDescription>
    </DialogHeader>

    <div class="flex min-w-0 flex-col gap-3 px-5 py-4">
      <div class="flex min-w-0 items-center gap-2">
        <Button
          type="button"
          size="icon"
          variant="outline"
          class="size-8 shrink-0"
          aria-label="Home folder"
          onclick={() => load(undefined)}
        >
          <House class="size-4" />
        </Button>
        <Button
          type="button"
          size="icon"
          variant="outline"
          class="size-8 shrink-0"
          aria-label="Up one folder"
          disabled={!parent}
          onclick={() => parent && load(parent)}
        >
          <CornerLeftUp class="size-4" />
        </Button>
        <code
          bind:this={pathEl}
          class="min-w-0 flex-1 overflow-x-auto whitespace-nowrap rounded-md border border-border bg-secondary px-2.5 py-1.5 text-xs"
        >
          {path || '…'}
        </code>
      </div>

      <div class="min-h-[12rem] max-h-[40vh] overflow-y-auto rounded-md border border-border">
        {#if loading}
          <div class="px-3 py-6 text-center text-sm text-muted-foreground">Loading…</div>
        {:else if error}
          <div class="px-3 py-6 text-center text-sm text-destructive">{error}</div>
        {:else if entries.length === 0}
          <div class="px-3 py-6 text-center text-sm text-muted-foreground">No subfolders here.</div>
        {:else}
          <ul class="divide-y divide-border">
            {#each entries as e (e.path)}
              <li>
                <button
                  type="button"
                  class="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-secondary/60"
                  onclick={() => load(e.path)}
                >
                  <Folder class="size-4 shrink-0 text-muted-foreground" />
                  <span class="truncate">{e.name}</span>
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    </div>

    <div class="flex items-center justify-end gap-2 border-t border-border px-5 py-3">
      <Button variant="ghost" onclick={() => (open = false)}>Cancel</Button>
      <Button onclick={selectCurrent} disabled={!path}>Select this folder</Button>
    </div>
  </DialogContent>
</Dialog>
