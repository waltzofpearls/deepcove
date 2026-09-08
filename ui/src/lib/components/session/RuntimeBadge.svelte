<script lang="ts">
  import Cpu from '@lucide/svelte/icons/cpu';
  import Container from '@lucide/svelte/icons/container';
  import { bootsVMs, runtimeLabel, runtimeShortLabel } from '$lib/util/runtime';
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';

  // Which sandbox a session runs in, sat alongside the agent tag. Worth surfacing because
  // the isolation boundary changes what a session can do: a VM has its own kernel and (today) no access
  // to your files, a container shares the host kernel.
  let { runtime, shared = false }: { runtime: string; shared?: boolean } = $props();

  // A VM gets the distinct icon because it is the choice worth noticing.
  const isVM = $derived(bootsVMs(runtime));
  // Same words as the runtimes page: a VM belongs to the workspace or to one session, never just "VM".
  const label = $derived(
    isVM ? (shared ? 'Workspace VM' : 'Session VM') : runtimeShortLabel(runtime),
  );
</script>

{#if runtime}
  <TooltipProvider>
    <Tooltip>
      <TooltipTrigger>
        {#snippet child({ props })}
          <span
            {...props}
            class="flex min-w-0 shrink-0 cursor-default items-center gap-1 rounded bg-sky-500/10 px-1.5 py-0.5 text-xs text-sky-700 ring-1 ring-inset ring-sky-600/25 dark:bg-sky-500/15 dark:text-sky-300 dark:ring-sky-500/30"
          >
            {#if isVM}
              <Cpu class="size-3 shrink-0" />
            {:else}
              <Container class="size-3 shrink-0" />
            {/if}
            <span class="truncate">{label}</span>
          </span>
        {/snippet}
      </TooltipTrigger>
      <TooltipContent>
        {#if shared}
          The workspace VM, which your other sessions use too
        {:else if isVM}
          A virtual machine for this session alone
        {:else}
          {runtimeLabel(runtime)}
        {/if}
      </TooltipContent>
    </Tooltip>
  </TooltipProvider>
{/if}
