<script lang="ts">
  import { Button } from '$lib/components/ui/button';
  import Logo from './Logo.svelte';
  import ThemeToggle from './ThemeToggle.svelte';
  import ViewToggle from './ViewToggle.svelte';
  import DeviceBar from './DeviceBar.svelte';
  import ProfileMenu from './ProfileMenu.svelte';
  import SessionTabBar from './SessionTabBar.svelte';
  import Menu from '@lucide/svelte/icons/menu';
  import Plus from '@lucide/svelte/icons/plus';

  let {
    onNewSession,
    onMenu,
  }: {
    onNewSession?: () => void;
    // Called when the user taps the mobile hamburger. The layout
    // owns the sidebar-drawer open state, and the topbar just signals.
    onMenu?: () => void;
  } = $props();
</script>

<header class="border-b border-border bg-card">
  <!-- Row 1: hamburger (mobile), brand, right cluster. Compressed on
       mobile via responsive utilities: ViewToggle hidden, the
       "New session" button drops its text label to icon-only. -->
  <div class="flex h-14 items-center justify-between gap-2 px-4">
    <div class="flex shrink-0 items-center gap-2">
      <Button
        variant="ghost"
        size="icon"
        class="md:hidden"
        onclick={onMenu}
        aria-label="Open menu"
      >
        <Menu class="size-5" />
      </Button>
      <a href="/" class="flex items-center text-foreground" aria-label="deepcove home">
        <Logo class="h-6 w-auto" />
      </a>
    </div>

    <div class="flex shrink-0 items-center gap-2">
      <!-- Your tailnet devices (session node + web node) with status and sign-in. -->
      <DeviceBar />
      <!-- Cards/list view toggle, a single compact icon button (like
           ThemeToggle), so it stays in the topbar on every breakpoint. -->
      <ViewToggle />
      <ThemeToggle />
      <!-- Mobile-only: the sidebar is a hidden drawer on phones, so the
           topbar keeps a "New session" button here. At md+ the sidebar
           is always visible and owns this action (see Sidebar). -->
      <Button size="sm" class="md:hidden" onclick={onNewSession} aria-label="New session">
        <Plus class="size-4" />
        <!-- Label hidden below sm to keep the right cluster compact
             on phones. The Plus icon + aria-label still carry intent. -->
        <span class="hidden sm:inline">New session</span>
      </Button>
      <ProfileMenu />
    </div>
  </div>

  <!-- Row 2: mobile session tabs. Whole second row, distinct from
       the topbar's global controls. Hidden on md+ where the sidebar
       submenu handles session switching. -->
  <SessionTabBar class="h-10 border-t border-border md:hidden" />
</header>
