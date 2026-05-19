<script lang="ts">
  import { page } from '$app/state';
  import { cn } from '$lib/utils';
  import LayoutGrid from '@lucide/svelte/icons/layout-grid';

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
</script>

<div class={cn('flex flex-col', className)}>
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
        </li>
      {/each}
    </ul>
  </nav>
  <div class="p-3 text-xs text-muted-foreground">
    <span class="font-mono">deepcove v0.0.1</span>
  </div>
</div>
