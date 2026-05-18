<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '$lib/components/ui/tooltip';
  import { preferences } from '$lib/stores/preferences.svelte';
  import LayoutGrid from '@lucide/svelte/icons/layout-grid';
  import List from '@lucide/svelte/icons/list';
</script>

<div class="inline-flex items-center rounded-md border border-border p-0.5">
  <TooltipProvider>
    <Tooltip>
      <TooltipTrigger>
        {#snippet child({ props })}
          <Button
            {...props}
            variant={preferences.viewMode === 'cards' ? 'secondary' : 'ghost'}
            size="icon"
            class="size-7"
            onclick={() => (preferences.viewMode = 'cards')}
            aria-label="Card view"
          >
            <LayoutGrid class="size-4" />
          </Button>
        {/snippet}
      </TooltipTrigger>
      <TooltipContent>Card view</TooltipContent>
    </Tooltip>

    <Tooltip>
      <TooltipTrigger>
        {#snippet child({ props })}
          <Button
            {...props}
            variant={preferences.viewMode === 'list' ? 'secondary' : 'ghost'}
            size="icon"
            class="size-7"
            onclick={() => (preferences.viewMode = 'list')}
            aria-label="List view"
          >
            <List class="size-4" />
          </Button>
        {/snippet}
      </TooltipTrigger>
      <TooltipContent>List view</TooltipContent>
    </Tooltip>
  </TooltipProvider>
</div>
