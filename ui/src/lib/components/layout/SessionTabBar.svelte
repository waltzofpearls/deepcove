<script lang="ts">
  import { page } from '$app/state';
  import { cn } from '$lib/utils';
  import { sessions } from '$lib/stores/sessions.svelte';
  import StatusIndicator from '$lib/components/session/StatusIndicator.svelte';
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';

  // Mobile-only horizontal tab strip for switching between sessions
  // without opening the sidebar drawer. The same data source the
  // desktop sidebar submenu uses (see $lib/stores/sessions.svelte).
  //
  // Subscribing here is ref-counted; if the dashboard / sidebar are
  // also mounted, all three share one poll. When this component
  // unmounts (e.g. layout flip past the md breakpoint hides it, or
  // the SPA navigates away), its subscription drops out.
  let { class: className = '' }: { class?: string } = $props();

  $effect(() => sessions.subscribe());

  function isCurrent(id: string): boolean {
    return page.url.pathname === `/sessions/${id}`;
  }
</script>

{#if sessions.list.length > 0}
  <!-- Wrapper applies the edge-fade mask so overflowing tabs hint
       at scrollability. Inline `style` because Tailwind v4 has no
       canonical mask-image utility; we want both prefixed forms for
       Safari < 18 and modern browsers. -->
  <div
    class={cn('relative h-full overflow-hidden', className)}
    style="mask-image: linear-gradient(to right, transparent 0, black 8px, black calc(100% - 8px), transparent 100%); -webkit-mask-image: linear-gradient(to right, transparent 0, black 8px, black calc(100% - 8px), transparent 100%);"
  >
    <div class="scrollbar-hide flex h-full items-center gap-1.5 overflow-x-auto px-2">
      <TooltipProvider>
        {#each sessions.list as s (s.id)}
          <Tooltip>
            <TooltipTrigger>
              {#snippet child({ props })}
                <a
                  {...props}
                  href="/sessions/{s.id}"
                  aria-label={s.name}
                  class={cn(
                    'inline-flex shrink-0 items-center gap-1.5 rounded-md border px-2 py-1 font-mono text-xs transition-colors',
                    // Inactive: subtle filled chip with a soft border
                    // so each tab reads as a discrete element even
                    // when names are similar. Hover bumps the fill.
                    // Active: full secondary fill + transparent
                    // border (matches the hit-area dimensions so
                    // tabs don't size-shift when active state flips).
                    isCurrent(s.id)
                      ? 'border-transparent bg-secondary text-foreground'
                      : 'border-border bg-secondary/40 text-muted-foreground hover:bg-secondary/70 hover:text-foreground',
                  )}
                >
                  <StatusIndicator status={s.status} />
                  <span class="max-w-[10ch] truncate">{s.name}</span>
                </a>
              {/snippet}
            </TooltipTrigger>
            <TooltipContent>{s.name}</TooltipContent>
          </Tooltip>
        {/each}
      </TooltipProvider>
    </div>
  </div>
{/if}

<style>
  /* Hide scrollbar but keep scrollability. Horizontal scrollbars
     eat precious vertical space on mobile, and the edge-fade mask
     already communicates scrollability. Touch swipe and mouse-wheel
     scroll still work without a visible bar. */
  :global(.scrollbar-hide) {
    scrollbar-width: none;
    -ms-overflow-style: none;
  }
  :global(.scrollbar-hide::-webkit-scrollbar) {
    display: none;
  }
</style>
