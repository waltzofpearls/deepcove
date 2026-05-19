<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import ThemeToggle from './ThemeToggle.svelte';
  import ViewToggle from './ViewToggle.svelte';
  import ProfileMenu from './ProfileMenu.svelte';
  import Menu from '@lucide/svelte/icons/menu';
  import Plus from '@lucide/svelte/icons/plus';

  let {
    onNewSession,
    onMenu,
  }: {
    onNewSession?: () => void;
    // Called when the user taps the mobile hamburger. The layout
    // owns the sidebar-drawer open state — the topbar just signals.
    onMenu?: () => void;
  } = $props();
</script>

<header class="flex h-14 items-center justify-between border-b border-border bg-card px-4">
  <div class="flex items-center gap-2">
    <!-- Hamburger: mobile only; opens the sidebar drawer via onMenu. -->
    <Button
      variant="ghost"
      size="icon"
      class="md:hidden"
      onclick={onMenu}
      aria-label="Open menu"
    >
      <Menu class="size-5" />
    </Button>
    <a href="/" class="flex items-center gap-2 font-mono text-base font-semibold tracking-tight">
      <span class="size-2.5 rounded-full bg-primary" aria-hidden="true"></span>
      deepcove
    </a>
  </div>

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
