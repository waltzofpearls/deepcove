<script lang="ts">
  import { page } from '$app/state';
  import LayoutGrid from '@lucide/svelte/icons/layout-grid';

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

<aside class="flex w-56 flex-col border-r border-border bg-card">
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
</aside>
