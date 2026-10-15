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
  import type { Me } from '$lib/api/types';
  import { avatarHue, initials } from '$lib/util/avatar';
  import UserCircle from '@lucide/svelte/icons/user-circle';

  // The person using the dashboard, as the node sees them. Null on a node that is not on a tailnet, so
  // the menu shows a "Local user" placeholder then.
  let me = $state<Me | null>(null);
  // pic is their picture as a data: URL, which the node fetches for us. pictureFor is the picture address
  // it came from, so a picture already shown is not fetched again. A failed one is tried again on the
  // next refresh.
  let pic = $state<string | undefined>(undefined);
  let pictureFor: string | undefined;

  async function refresh() {
    me = await api.me().catch(() => null);
    const wanted = me?.profile_pic_url;
    if (wanted && wanted === pictureFor && pic) return;
    pic = wanted ? ((await api.myProfilePicture().catch(() => null)) ?? undefined) : undefined;
    pictureFor = wanted;
  }

  // Load once on mount, and refresh whenever the menu opens so it reflects a
  // tailnet connection made after the app loaded.
  $effect(() => {
    void refresh();
  });

  const name = $derived(me ? me.display_name || me.login_name : 'Local user');
  const sub = $derived(me ? me.login_name : 'local');
</script>

{#snippet avatar(sizeClass: string, textClass: string)}
  {#if pic}
    <img
      src={pic}
      alt=""
      class="{sizeClass} shrink-0 rounded-full object-cover"
      onerror={() => (pic = undefined)}
    />
  {:else if me}
    <span
      class="{sizeClass} {textClass} flex shrink-0 items-center justify-center rounded-full font-medium text-white"
      style:background-color="hsl({avatarHue(me.login_name)} 45% 32%)"
    >
      {initials(me.display_name, me.login_name)}
    </span>
  {:else}
    <UserCircle class="{sizeClass} shrink-0 text-muted-foreground" />
  {/if}
{/snippet}

<DropdownMenu onOpenChange={(open) => open && refresh()}>
  <DropdownMenuTrigger>
    {#snippet child({ props })}
      <Button {...props} variant="ghost" size="icon" class="rounded-full" aria-label="Profile menu">
        {@render avatar('size-6', 'text-[10px]')}
      </Button>
    {/snippet}
  </DropdownMenuTrigger>
  <DropdownMenuContent align="end" class="w-56">
    <DropdownMenuLabel>
      <div class="flex items-center gap-2.5">
        {@render avatar('size-8', 'text-xs')}
        <div class="flex min-w-0 flex-col">
          <span class="truncate text-sm font-medium">{name}</span>
          <span class="truncate font-mono text-xs text-muted-foreground">{sub}</span>
        </div>
      </div>
    </DropdownMenuLabel>
    <DropdownMenuSeparator />
    <DropdownMenuItem onclick={() => goto('/setup')}>Set up deepcove</DropdownMenuItem>
    <DropdownMenuItem onclick={() => goto('/settings')}>Settings</DropdownMenuItem>
  </DropdownMenuContent>
</DropdownMenu>
