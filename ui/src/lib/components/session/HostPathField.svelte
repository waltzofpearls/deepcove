<script lang="ts">
  import { untrack } from 'svelte';
  import { Input } from '$lib/components/ui/input';
  import { Button } from '$lib/components/ui/button';
  import { api } from '$lib/api/client';
  import type { FsEntry, SessionHost } from '$lib/api/types';
  import { splitPath } from '$lib/util/path';
  import FolderPicker from './FolderPicker.svelte';
  import Folder from '@lucide/svelte/icons/folder';
  import FolderOpen from '@lucide/svelte/icons/folder-open';

  // Host-path input with directory autocomplete + a Browse modal. value is the
  // absolute host path (two-way). Listing is scoped to $HOME server-side.
  // host (optional) browses a remote node's filesystem (editing a peer-hosted session's mounts).
  let {
    value = $bindable(''),
    id,
    placeholder = '/Users/you/code/myrepo',
    host,
  }: { value?: string; id?: string; placeholder?: string; host?: SessionHost } = $props();

  // Render up to MAX_SUGGEST (the dropdown scrolls); moreCount and the header
  // make it read as a filter of one directory, not the only folders you're
  // allowed.
  const MAX_SUGGEST = 50;

  let pickerOpen = $state(false);
  let suggestions = $state<FsEntry[]>([]);
  let moreCount = $state(0);
  let listedDir = $state('');
  let showSuggest = $state(false);
  let focused = $state(false);
  let activeIndex = $state(-1);
  // The value the user just chose from the dropdown. The suggestion effect watches `value`, so without
  // this, picking an entry immediately refetches and reopens the list showing what was just picked.
  let picked = $state('');

  // Home differs per node, so a path from one machine is refused by the next.
  let seededFor = $state<string | null>(null);

  // An empty field means the first keystroke asks for "/", so start at home instead. A path the parent
  // bound is left alone: only a change of node replaces it, because a path from one machine is refused
  // by the next.
  $effect(() => {
    const key = host?.dns_name ?? '';
    const seeded = untrack(() => seededFor);
    const bound = untrack(() => value);
    // Never seeded is not a change of node. Reading it as one is what overwrote a saved folder a moment
    // after the dialog opened.
    const nodeChanged = seeded !== null && seeded !== key;
    if (bound !== '' && !nodeChanged) {
      seededFor = key;
      return;
    }
    void api
      .fsList(undefined, host)
      .then((res) =>
        untrack(() => {
          if (value === '' || nodeChanged) value = res.path + '/';
          seededFor = key;
        }),
      )
      .catch(() => {});
  });

  // Debounced autocomplete, best-effort: any error just yields no suggestions.
  $effect(() => {
    const v = value;
    if (!focused) return;
    if (v === picked) return;
    let cancelled = false;
    const timer = setTimeout(async () => {
      const { dir, prefix } = splitPath(v);
      try {
        const res = await api.fsList(dir, host);
        if (cancelled) return;
        const p = prefix.toLowerCase();
        const filtered = res.entries.filter((e) => e.name.toLowerCase().startsWith(p));
        suggestions = filtered.slice(0, MAX_SUGGEST);
        moreCount = filtered.length - suggestions.length;
        listedDir = res.path;
        showSuggest = suggestions.length > 0;
        activeIndex = -1;
      } catch {
        if (!cancelled) {
          suggestions = [];
          moreCount = 0;
          showSuggest = false;
        }
      }
    }, 200);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  });

  function pick(entry: FsEntry) {
    picked = entry.path;
    value = entry.path;
    showSuggest = false;
  }

  function onKeydown(e: KeyboardEvent) {
    if (!showSuggest) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      activeIndex = Math.min(activeIndex + 1, suggestions.length - 1);
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      activeIndex = Math.max(activeIndex - 1, 0);
    } else if (e.key === 'Enter' && activeIndex >= 0) {
      e.preventDefault();
      pick(suggestions[activeIndex]);
    } else if (e.key === 'Escape') {
      showSuggest = false;
    }
  }
</script>

<div class="relative">
  <div class="flex gap-2">
    <Input
      {id}
      bind:value
      {placeholder}
      autocomplete="off"
      spellcheck={false}
      oninput={() => (picked = '')}
      onfocus={() => (focused = true)}
      onblur={() => {
        focused = false;
        // Delay so a suggestion mousedown registers before the list hides.
        setTimeout(() => (showSuggest = false), 120);
      }}
      onkeydown={onKeydown}
    />
    <Button type="button" variant="outline" class="shrink-0" onclick={() => (pickerOpen = true)}>
      <FolderOpen class="size-4" />
      Browse
    </Button>
  </div>

  {#if showSuggest}
    <div
      class="absolute z-50 mt-1 w-full overflow-hidden rounded-md border border-border bg-background shadow-md"
    >
      <div class="border-b border-border px-2 py-1.5 text-[11px] text-muted-foreground">
        Folders in <code class="text-foreground">{listedDir || '~'}</code>
        . Type to filter
      </div>
      <ul class="max-h-56 overflow-auto p-1">
        {#each suggestions as s, i (s.path)}
          <li>
            <button
              type="button"
              class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-secondary {i ===
              activeIndex
                ? 'bg-secondary'
                : ''}"
              onmousedown={(e) => {
                e.preventDefault();
                pick(s);
              }}
            >
              <Folder class="size-3.5 shrink-0 text-muted-foreground" />
              <span class="truncate">{s.name}</span>
            </button>
          </li>
        {/each}
      </ul>
      {#if moreCount > 0}
        <div class="border-t border-border px-2 py-1.5 text-[11px] text-muted-foreground">
          +{moreCount} more. Keep typing to filter, or use Browse
        </div>
      {/if}
    </div>
  {/if}

  <FolderPicker bind:open={pickerOpen} bind:value start={value} {host} />
</div>
