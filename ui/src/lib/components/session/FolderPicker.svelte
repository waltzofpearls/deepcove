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
  import { api, ApiError } from '$lib/api/client';
  import type { FsEntry, SessionHost } from '$lib/api/types';
  import { pickerQuery } from '$lib/util/path';
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
  let pathEl = $state<HTMLInputElement | null>(null);
  // What is in the path box. loadedPath is what load() last put there, so the typing effect skips it.
  let enteredPath = $state('');
  let loadedPath = '';

  const prefix = $derived(pickerQuery(enteredPath).prefix.toLowerCase());
  const visible = $derived(entries.filter((entry) => entry.name.toLowerCase().startsWith(prefix)));
  const enteredFolder = $derived(
    prefix ? entries.find((entry) => entry.name.toLowerCase() === prefix) : undefined,
  );

  async function load(p?: string) {
    loading = true;
    error = null;
    try {
      const res = await api.fsList(p, host);
      path = res.path;
      parent = res.parent;
      entries = res.entries;
      enteredPath = res.path === '/' ? '/' : res.path + '/';
      loadedPath = enteredPath;
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

  // Typing lists the folder before the last slash. A folder that isn't there lists nothing.
  $effect(() => {
    const text = enteredPath;
    if (!open || text === loadedPath) return;
    const timer = setTimeout(async () => {
      try {
        const res = await api.fsList(pickerQuery(text).dir, host);
        if (enteredPath !== text) return;
        path = res.path;
        parent = res.parent;
        entries = res.entries;
        error = null;
      } catch {
        if (enteredPath !== text) return;
        path = '';
        entries = [];
      }
    }, 200);
    return () => clearTimeout(timer);
  });

  // Keep the path box scrolled to the end so the current (deepest) folder is
  // visible rather than the /Users/... prefix.
  $effect(() => {
    if (pathEl && enteredPath === loadedPath) pathEl.scrollLeft = pathEl.scrollWidth;
  });

  function onPathKeydown(event: KeyboardEvent) {
    if (event.key !== 'Tab' && event.key !== 'Enter') return;
    if (!prefix || visible.length === 0) return;
    event.preventDefault();
    load(visible[0].path);
  }

  function selectCurrent() {
    value = enteredFolder?.path ?? path;
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
        <Input
          bind:ref={pathEl}
          bind:value={enteredPath}
          class="min-w-0 flex-1 font-mono text-xs md:text-xs"
          aria-label="Folder path"
          autocomplete="off"
          spellcheck={false}
          onkeydown={onPathKeydown}
        />
      </div>

      <div class="min-h-[12rem] max-h-[40vh] overflow-y-auto rounded-md border border-border">
        {#if loading}
          <div class="px-3 py-6 text-center text-sm text-muted-foreground">Loading…</div>
        {:else if error}
          <div class="px-3 py-6 text-center text-sm text-destructive">{error}</div>
        {:else if visible.length === 0}
          <div class="px-3 py-6 text-center text-sm text-muted-foreground">
            {prefix ? 'No matching folders.' : 'No subfolders here.'}
          </div>
        {:else}
          <ul class="divide-y divide-border">
            {#each visible as e (e.path)}
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
      <Button onclick={selectCurrent} disabled={!path || (prefix !== '' && !enteredFolder)}>
        Select this folder
      </Button>
    </div>
  </DialogContent>
</Dialog>
