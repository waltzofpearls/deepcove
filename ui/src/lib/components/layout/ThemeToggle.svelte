<script lang="ts">
  import { mode, setMode, userPrefersMode } from 'mode-watcher';
  import { Button } from '$lib/components/ui/button';
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';
  import Sun from '@lucide/svelte/icons/sun';
  import Moon from '@lucide/svelte/icons/moon';
  import Monitor from '@lucide/svelte/icons/monitor';

  // Cycle: system → light → dark → system. Shows the icon of the
  // user's *intent* (system / light / dark), not the resolved mode,
  // so toggling feels predictable.
  function cycle() {
    const current = userPrefersMode.current;
    if (current === 'system') setMode('light');
    else if (current === 'light') setMode('dark');
    else setMode('system');
  }

  let label = $derived(
    userPrefersMode.current === 'system'
      ? `Theme: system (currently ${mode.current})`
      : `Theme: ${userPrefersMode.current}`,
  );
</script>

<TooltipProvider>
  <Tooltip>
    <TooltipTrigger>
      {#snippet child({ props })}
        <Button {...props} variant="ghost" size="icon" onclick={cycle} aria-label={label}>
          {#if userPrefersMode.current === 'system'}
            <Monitor class="size-4" />
          {:else if userPrefersMode.current === 'light'}
            <Sun class="size-4" />
          {:else}
            <Moon class="size-4" />
          {/if}
        </Button>
      {/snippet}
    </TooltipTrigger>
    <TooltipContent>{label}</TooltipContent>
  </Tooltip>
</TooltipProvider>
