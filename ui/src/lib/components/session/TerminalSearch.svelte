<script lang="ts">
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import ChevronUp from '@lucide/svelte/icons/chevron-up';
  import Search from '@lucide/svelte/icons/search';
  import X from '@lucide/svelte/icons/x';
  import { searchKeyAction, type SearchDirection } from '$lib/util/terminalSearch';

  let {
    problem,
    onSearch,
    onEdit,
    onClose,
  }: {
    // problem is why the last search showed nothing: no matches, or the search failing.
    problem: string | undefined;
    onSearch: (text: string, direction: SearchDirection) => void;
    // onEdit is the text changing, which makes the last answer stale.
    onEdit: () => void;
    onClose: () => void;
  } = $props();

  let text = $state('');
  let input: HTMLInputElement;

  export function focus() {
    input.focus();
  }

  function search(direction: SearchDirection) {
    if (text) onSearch(text, direction);
  }

  function onkeydown(event: KeyboardEvent) {
    const action = searchKeyAction(event);
    if (!action) return;
    event.preventDefault();
    if (action === 'close') onClose();
    else search(action);
  }
</script>

<!-- Full width under the header on a phone, so it stays in view above the keyboard. A floating bar at the
     top right on a wider screen. -->
<div
  role="search"
  class="absolute inset-x-0 top-0 z-20 flex items-center gap-0.5 border-b border-border bg-card px-2 py-2 md:inset-x-auto md:right-4 md:top-2 md:gap-1 md:rounded-lg md:border md:p-1.5 md:shadow-lg"
>
  <label
    class="flex h-10 min-w-0 flex-1 items-center gap-2 rounded-md border bg-background px-2.5 md:h-7 md:w-60 md:flex-none {problem
      ? 'border-destructive'
      : 'border-primary'}"
  >
    <Search class="size-4 shrink-0 text-muted-foreground md:size-3.5" aria-hidden="true" />
    <span class="sr-only">Search terminal history</span>
    <!-- 16px text on a phone, so iOS does not zoom the page when the field gets focus. -->
    <input
      bind:this={input}
      bind:value={text}
      {onkeydown}
      oninput={onEdit}
      placeholder="Search history"
      aria-invalid={!!problem}
      class="min-w-0 flex-1 bg-transparent text-base text-foreground outline-none placeholder:text-muted-foreground md:text-sm"
    />
  </label>
  {#if problem}
    <span role="status" class="shrink-0 px-1 text-xs text-destructive">{problem}</span>
  {/if}
  <button
    type="button"
    onclick={() => search('older')}
    disabled={!text}
    aria-label="Older match"
    title="Older match (Enter)"
    class="inline-flex size-11 shrink-0 items-center justify-center rounded-md text-foreground hover:bg-secondary disabled:opacity-40 md:size-7"
  >
    <ChevronUp class="size-5 md:size-4" />
  </button>
  <button
    type="button"
    onclick={() => search('newer')}
    disabled={!text}
    aria-label="Newer match"
    title="Newer match (Shift Enter)"
    class="inline-flex size-11 shrink-0 items-center justify-center rounded-md text-foreground hover:bg-secondary disabled:opacity-40 md:size-7"
  >
    <ChevronDown class="size-5 md:size-4" />
  </button>
  <button
    type="button"
    onclick={onClose}
    aria-label="Close search"
    title="Close (Esc)"
    class="inline-flex size-11 shrink-0 items-center justify-center rounded-md text-foreground hover:bg-secondary md:size-7"
  >
    <X class="size-5 md:size-4" />
  </button>
</div>
