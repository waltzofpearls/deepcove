<script lang="ts">
  import { getContext } from 'svelte';
  import { page } from '$app/state';
  import { cn } from '$lib/utils';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { NEW_SESSION_CTX } from '$lib/context';
  import { Button } from '$lib/components/ui/button';
  import {
    Tooltip,
    TooltipContent,
    TooltipProvider,
    TooltipTrigger,
  } from '$lib/components/ui/tooltip';
  import StatusIndicator from '$lib/components/session/StatusIndicator.svelte';
  import LogoIcon from './LogoIcon.svelte';
  import LayoutGrid from '@lucide/svelte/icons/layout-grid';
  import Server from '@lucide/svelte/icons/server';
  import Box from '@lucide/svelte/icons/box';
  import Plus from '@lucide/svelte/icons/plus';

  // Opens the New Session dialog mounted in +layout.svelte (and closes the mobile drawer). Same
  // primary action as the topbar, pinned to the top of the sidebar as the prominent CTA.
  const openNewSession = getContext<() => void>(NEW_SESSION_CTX);

  // The sidebar is rendered as a passthrough container; the layout decides whether to wrap it in a
  // static aside (desktop) or a Sheet drawer (mobile). The `class` prop is merged via cn() so each
  // call site can supply its own width / border / visibility rules without forking the component.
  let { class: className = '' }: { class?: string } = $props();

  // Top-level destinations. "Sessions" is NOT one of these: it's a non-link group (below) whose
  // children are the live sessions, since Dashboard already is the sessions canvas. The open-ended
  // sessions list sits last so it doesn't push these fixed links around as sessions come and go.
  const navItems: Array<{ href: string; label: string; icon: typeof LayoutGrid }> = [
    { href: '/', label: 'Dashboard', icon: LayoutGrid },
    { href: '/nodes', label: 'Nodes', icon: Server },
    { href: '/runtimes', label: 'Runtimes', icon: Box },
  ];

  function isActive(href: string): boolean {
    if (href === '/') return page.url.pathname === '/';
    return page.url.pathname.startsWith(href);
  }

  // Keep the sessions poll alive whenever the sidebar is mounted so the Sessions group stays live
  // from any route. The store is ref-counted, so this shares the dashboard's poll when both mount.
  $effect(() => sessions.subscribe());

  function isCurrentSession(id: string): boolean {
    return page.url.pathname === `/sessions/${id}`;
  }
</script>

<div class={cn('flex flex-col', className)}>
  <!-- Primary CTA pinned above the nav, desktop only. The topbar drops its own "New session" button
       at md+ (see TopBar), so the sidebar is the single desktop entry point. On mobile the sidebar is
       a drawer (a full-width button there looks oversized), so this is hidden and the topbar's icon
       button handles it. -->
  <div class="hidden p-3 pb-0 md:block">
    <!-- badge-style teal (matches the remote-node tag), lighter than solid-primary -->
    <Button
      class="w-full justify-start bg-teal-500/15 text-teal-800 ring-1 ring-inset ring-teal-600/35 hover:bg-teal-500/25 hover:text-teal-900 dark:bg-teal-500/15 dark:text-teal-300 dark:ring-teal-500/30 dark:hover:bg-teal-500/25 dark:hover:text-teal-200"
      onclick={openNewSession}
    >
      <Plus class="size-4" />
      New session
    </Button>
  </div>

  <nav class="flex-1 overflow-y-auto p-3">
    <ul class="space-y-1">
      {#each navItems as item}
        {@const Icon = item.icon}
        <li>
          <a
            href={item.href}
            class="flex items-center gap-2 rounded-md px-3 py-2 text-sm transition-colors"
            class:bg-secondary={isActive(item.href)}
            class:text-foreground={isActive(item.href)}
            class:text-muted-foreground={!isActive(item.href)}
            class:hover:bg-secondary={!isActive(item.href)}
            class:hover:text-foreground={!isActive(item.href)}
          >
            <Icon class="size-4" />
            {item.label}
          </a>
        </li>
      {/each}
    </ul>

    {#if sessions.list.length > 0}
      <!-- Sessions group: a non-link section label with the live sessions as children. Lives at the
           top level (not nested under Dashboard) so you can hop between sessions from any route. -->
      <div class="mt-5">
        <div class="px-3 pb-1 text-xs font-medium tracking-wide text-muted-foreground">
          Sessions
        </div>
        <ul class="space-y-0.5">
          <TooltipProvider>
            {#each sessions.list as s (s.id)}
              <li>
                <Tooltip>
                  <TooltipTrigger>
                    {#snippet child({ props })}
                      <a
                        {...props}
                        href="/sessions/{s.id}"
                        class="flex items-center gap-2 rounded-md px-3 py-1.5 text-sm transition-colors"
                        class:bg-secondary={isCurrentSession(s.id)}
                        class:text-foreground={isCurrentSession(s.id)}
                        class:text-muted-foreground={!isCurrentSession(s.id)}
                        class:hover:bg-secondary={!isCurrentSession(s.id)}
                        class:hover:text-foreground={!isCurrentSession(s.id)}
                      >
                        <StatusIndicator status={s.status} />
                        <span class="truncate font-mono">{s.name}</span>
                      </a>
                    {/snippet}
                  </TooltipTrigger>
                  <TooltipContent side="right">{s.name}</TooltipContent>
                </Tooltip>
              </li>
            {/each}
          </TooltipProvider>
        </ul>
      </div>
    {/if}
  </nav>
  <div class="flex items-center gap-1.5 p-3 text-xs text-muted-foreground">
    <LogoIcon class="size-3.5" />
    <span class="font-mono">deepcove v0.0.1</span>
  </div>
</div>
