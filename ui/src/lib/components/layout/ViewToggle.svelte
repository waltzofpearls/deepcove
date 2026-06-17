<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '$lib/components/ui/tooltip';
  import { preferences } from '$lib/stores/preferences.svelte';
  import LayoutGrid from '@lucide/svelte/icons/layout-grid';
  import List from '@lucide/svelte/icons/list';

  // Single-button toggle, mirroring ThemeToggle: the icon shows the
  // *current* view (grid when on cards, list when on list); clicking
  // flips to the other.
  function toggle() {
    preferences.viewMode = preferences.viewMode === 'cards' ? 'list' : 'cards';
  }

  let label = $derived(preferences.viewMode === 'cards' ? 'Card view' : 'List view');
</script>

<TooltipProvider>
  <Tooltip>
    <TooltipTrigger>
      {#snippet child({ props })}
        <Button {...props} variant="ghost" size="icon" onclick={toggle} aria-label={label}>
          {#if preferences.viewMode === 'cards'}
            <LayoutGrid class="size-4" />
          {:else}
            <List class="size-4" />
          {/if}
        </Button>
      {/snippet}
    </TooltipTrigger>
    <TooltipContent>{label}</TooltipContent>
  </Tooltip>
</TooltipProvider>
