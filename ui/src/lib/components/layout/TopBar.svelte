<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import ThemeToggle from './ThemeToggle.svelte';
  import ViewToggle from './ViewToggle.svelte';
  import ProfileMenu from './ProfileMenu.svelte';
  import Plus from '@lucide/svelte/icons/plus';

  let { onNewSession }: { onNewSession?: () => void } = $props();
</script>

<header class="flex h-14 items-center justify-between border-b border-border bg-card px-4">
  <a href="/" class="flex items-center gap-2 font-mono text-base font-semibold tracking-tight">
    <span class="size-2.5 rounded-full bg-primary" aria-hidden="true"></span>
    deepcove
  </a>

  <div class="flex items-center gap-2">
    <ViewToggle />
    <ThemeToggle />
    <Button size="sm" onclick={onNewSession}>
      <Plus class="size-4" />
      <span>New session</span>
    </Button>
    <ProfileMenu />
  </div>
</header>
