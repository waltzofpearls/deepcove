<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuLabel,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
  } from '$lib/components/ui/dropdown-menu';
  import { goto } from '$app/navigation';
  import { api } from '$lib/api/client';
  import type { ConnectOwner } from '$lib/api/types';
  import UserCircle from '@lucide/svelte/icons/user-circle';

  // Current viewer's identity. Tailnet access is owner-gated, so the node owner
  // from the connect status IS the viewer. Null when not on a tailnet (rung 1 /
  // local, or the connect endpoint is absent) — we show a "Local user"
  // placeholder then. (Per-request WhoIs via /api/me is the right source once
  // per-user sharing lands; until then owner == viewer.)
  let owner = $state<ConnectOwner | null>(null);
  let imgFailed = $state(false);

  async function refresh() {
    imgFailed = false;
    try {
      owner = (await api.connectStatus()).owner ?? null;
    } catch {
      owner = null;
    }
  }

  // Load once on mount, and refresh whenever the menu opens so it reflects a
  // tailnet connection made after the app loaded.
  $effect(() => {
    void refresh();
  });

  const name = $derived(owner ? owner.display_name || owner.login_name : 'Local user');
  const sub = $derived(owner ? owner.login_name : 'local');
  const pic = $derived(owner && !imgFailed ? owner.profile_pic_url : undefined);
</script>

<DropdownMenu onOpenChange={(open) => open && refresh()}>
  <DropdownMenuTrigger>
    {#snippet child({ props })}
      <Button {...props} variant="ghost" size="icon" class="rounded-full" aria-label="Profile menu">
        {#if pic}
          <img
            src={pic}
            alt=""
            class="size-6 rounded-full object-cover"
            onerror={() => (imgFailed = true)}
          />
        {:else}
          <UserCircle class="size-5" />
        {/if}
      </Button>
    {/snippet}
  </DropdownMenuTrigger>
  <DropdownMenuContent align="end" class="w-56">
    <DropdownMenuLabel>
      <div class="flex items-center gap-2.5">
        {#if pic}
          <img
            src={pic}
            alt=""
            class="size-8 shrink-0 rounded-full object-cover"
            onerror={() => (imgFailed = true)}
          />
        {:else}
          <UserCircle class="size-8 shrink-0 text-muted-foreground" />
        {/if}
        <div class="flex min-w-0 flex-col">
          <span class="truncate text-sm font-medium">{name}</span>
          <span class="truncate font-mono text-xs text-muted-foreground">{sub}</span>
        </div>
      </div>
    </DropdownMenuLabel>
    <DropdownMenuSeparator />
    <DropdownMenuItem onclick={() => goto('/settings')}>Settings</DropdownMenuItem>
  </DropdownMenuContent>
</DropdownMenu>
