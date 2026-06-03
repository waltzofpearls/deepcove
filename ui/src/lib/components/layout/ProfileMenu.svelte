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
  import UserCircle from '@lucide/svelte/icons/user-circle';

  // Owner identity isn't wired yet — until tsnet WhoIs middleware lands
  // we show a placeholder. Search for `placeholderOwner` in the Go side.
  const ownerLabel = 'local';
</script>

<DropdownMenu>
  <DropdownMenuTrigger>
    {#snippet child({ props })}
      <Button {...props} variant="ghost" size="icon" class="rounded-full" aria-label="Profile menu">
        <UserCircle class="size-5" />
      </Button>
    {/snippet}
  </DropdownMenuTrigger>
  <DropdownMenuContent align="end" class="w-48">
    <DropdownMenuLabel>
      <div class="flex flex-col">
        <span class="text-sm font-medium">Owner</span>
        <span class="text-xs text-muted-foreground font-mono">{ownerLabel}</span>
      </div>
    </DropdownMenuLabel>
    <DropdownMenuSeparator />
    <DropdownMenuItem onclick={() => goto('/settings')}>Settings</DropdownMenuItem>
  </DropdownMenuContent>
</DropdownMenu>
