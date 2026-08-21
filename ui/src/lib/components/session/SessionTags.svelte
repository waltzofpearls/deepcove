<script lang="ts">
  import type { Session } from '$lib/api/types';
  import { sessions } from '$lib/stores/sessions.svelte';
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';
  import ClusterBadge from '$lib/components/ClusterBadge.svelte';
  import RuntimeBadge from '$lib/components/session/RuntimeBadge.svelte';
  import Globe from '@lucide/svelte/icons/globe';
  import Waypoints from '@lucide/svelte/icons/waypoints';

  // Compact identity tags (agent + remote-node + runtime + cluster), shared by the card and session view.
  // elevated raises the agent badge above the card's full-card nav link so its tooltip stays hoverable.
  let { session, elevated = false }: { session: Session; elevated?: boolean } = $props();

  const remoteName = $derived(sessions.hostLabel(session));
</script>

<div class="flex min-w-0 items-center gap-1.5">
  <TooltipProvider>
    <Tooltip>
      <TooltipTrigger>
        {#snippet child({ props })}
          <span
            {...props}
            class="shrink-0 cursor-default rounded bg-secondary px-1.5 py-0.5 text-xs font-medium text-secondary-foreground ring-1 ring-inset ring-muted-foreground/30 dark:bg-white/10"
            class:relative={elevated}
            class:z-20={elevated}
          >
            {session.agent}
          </span>
        {/snippet}
      </TooltipTrigger>
      <TooltipContent>Session {session.id}</TooltipContent>
    </Tooltip>
  </TooltipProvider>
  {#if remoteName}
    <span
      class="flex min-w-0 items-center gap-1 rounded bg-teal-500/15 px-1.5 py-0.5 text-xs text-teal-800 ring-1 ring-inset ring-teal-600/35 dark:bg-teal-500/15 dark:text-teal-300 dark:ring-teal-500/30"
    >
      <Globe class="size-3 shrink-0" />
      <span class="truncate">{remoteName}</span>
    </span>
  {/if}
  <RuntimeBadge runtime={session.runtime} shared={session.on_workspace_vm ?? false} />
  {#if session.use_llm_proxy}
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger>
          {#snippet child({ props })}
            <span
              {...props}
              class="flex shrink-0 cursor-default items-center gap-1 rounded bg-amber-500/10 px-1.5 py-0.5 text-xs text-amber-700 ring-1 ring-inset ring-amber-600/25 dark:bg-amber-500/15 dark:text-amber-300 dark:ring-amber-500/30"
            >
              <Waypoints class="size-3 shrink-0" />
              <span>LLM proxy</span>
            </span>
          {/snippet}
        </TooltipTrigger>
        <TooltipContent>This session's API requests go through this node's LLM proxy</TooltipContent
        >
      </Tooltip>
    </TooltipProvider>
  {/if}
  <ClusterBadge cluster={session.host?.cluster ?? ''} />
</div>
