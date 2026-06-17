<script lang="ts">
  import { getContext } from 'svelte';
  import { page } from '$app/state';
  import { cn } from '$lib/utils';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { NEW_SESSION_CTX } from '$lib/context';
  import { Button } from '$lib/components/ui/button';
  import StatusIndicator from '$lib/components/session/StatusIndicator.svelte';
  import LogoIcon from './LogoIcon.svelte';
  import LayoutGrid from '@lucide/svelte/icons/layout-grid';
  import Plus from '@lucide/svelte/icons/plus';

  // Opens the New Session dialog mounted in +layout.svelte (and closes
  // the mobile drawer). Same primary action as the topbar — pinned to
  // the top of the sidebar as the prominent CTA.
  const openNewSession = getContext<() => void>(NEW_SESSION_CTX);

  // The sidebar is rendered as a passthrough container — the layout
  // decides whether to wrap it in a static aside (desktop) or a
  // Sheet drawer (mobile). The `class` prop is merged via cn() so
  // each call site can supply its own width / border / visibility
  // rules without forking the component.
  let { class: className = '' }: { class?: string } = $props();

  // Sidebar is intentionally minimal in v0 — just Dashboard.
  // Nodes / Shared / Settings will join as those features ship.
  const navItems: Array<{ href: string; label: string; icon: typeof LayoutGrid }> = [
    { href: '/', label: 'Dashboard', icon: LayoutGrid },
  ];

  function isActive(href: string): boolean {
    if (href === '/') return page.url.pathname === '/';
    return page.url.pathname.startsWith(href);
  }

  // Sessions submenu: only renders when the user is inside a
  // session detail route. The data comes from the shared sessions
  // store (extracted in slice-1 step 1 for exactly this consumer);
  // subscribing here keeps the poll alive while in a session — when
  // the dashboard isn't mounted but the sidebar is the only viewer.
  const inSession = $derived(page.url.pathname.startsWith('/sessions/'));
  $effect(() => {
    if (inSession) return sessions.subscribe();
  });

  function isCurrentSession(id: string): boolean {
    return page.url.pathname === `/sessions/${id}`;
  }
</script>

<div class={cn('flex flex-col', className)}>
  <!-- Primary CTA pinned above the nav, desktop only. The topbar drops
       its own "New session" button at md+ (see TopBar), so the sidebar
       is the single desktop entry point. On mobile the sidebar is a
       drawer (a full-width button there looks oversized), so this is
       hidden and the topbar's icon button handles it. -->
  <div class="hidden p-3 pb-0 md:block">
    <Button class="w-full justify-start" onclick={openNewSession}>
      <Plus class="size-4" />
      New session
    </Button>
  </div>

  <nav class="flex-1 p-3">
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

          {#if inSession && item.href === '/'}
            <!-- Sessions submenu: only shows under Dashboard when
                 the user is in a session detail route. Hidden on the
                 dashboard itself (the cards / list view already
                 surfaces every session). -->
            <ul class="ml-2 mt-1 space-y-0.5 border-l border-border pl-2">
              {#each sessions.list as s (s.id)}
                <li>
                  <a
                    href="/sessions/{s.id}"
                    class="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs transition-colors"
                    class:bg-secondary={isCurrentSession(s.id)}
                    class:text-foreground={isCurrentSession(s.id)}
                    class:text-muted-foreground={!isCurrentSession(s.id)}
                    class:hover:bg-secondary={!isCurrentSession(s.id)}
                    class:hover:text-foreground={!isCurrentSession(s.id)}
                  >
                    <StatusIndicator status={s.status} />
                    <span class="truncate font-mono">{s.name}</span>
                  </a>
                </li>
              {/each}
            </ul>
          {/if}
        </li>
      {/each}
    </ul>
  </nav>
  <div class="flex items-center gap-1.5 p-3 text-xs text-muted-foreground">
    <LogoIcon class="size-3.5" />
    <span class="font-mono">deepcove v0.0.1</span>
  </div>
</div>
