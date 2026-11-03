<script lang="ts">
  // Shown when deepcove refuses this browser. `deepcove open` unlocks it, and the page reloads by itself.
  import { onMount } from 'svelte';
  import { waitForUnlock } from '$lib/api/localLock';
  import Logo from './layout/Logo.svelte';

  let copiedCommand = $state('');

  onMount(() => waitForUnlock());

  async function copy(command: string) {
    try {
      await navigator.clipboard.writeText(command);
    } catch {
      return;
    }
    copiedCommand = command;
    setTimeout(() => {
      copiedCommand = '';
    }, 1500);
  }
</script>

{#snippet commandBox(command: string)}
  <div
    class="flex items-center gap-2 rounded-lg border border-border bg-muted py-1.5 pr-1.5 pl-3.5"
  >
    <code class="min-w-0 flex-1 font-mono text-[15px]">{command}</code>
    <button
      type="button"
      onclick={() => copy(command)}
      class="h-9 min-w-[76px] rounded-md border border-border bg-card px-3.5 text-[13px] font-medium transition-colors hover:bg-secondary"
    >
      {copiedCommand === command ? 'Copied' : 'Copy'}
    </button>
  </div>
{/snippet}

<div class="flex min-h-dvh items-center justify-center bg-background px-4 py-12 text-foreground">
  <div class="flex w-full max-w-[440px] flex-col gap-6 rounded-xl border border-border bg-card p-8">
    <Logo class="h-[30px] w-auto self-start" />
    <div class="flex flex-col gap-2">
      <h1 class="text-[22px] leading-tight font-semibold">Unlock this browser</h1>
      <p class="text-sm text-muted-foreground">Run this command in a terminal on this computer:</p>
    </div>
    {@render commandBox('deepcove open')}
    <div class="flex items-center gap-2.5 text-[13px] text-muted-foreground">
      <span class="size-2 shrink-0 rounded-full bg-primary"></span>
      This page unlocks by itself after you run it.
    </div>
    <div class="flex flex-col gap-2.5">
      <p class="text-sm text-muted-foreground">
        Not your default browser? Run this instead, then paste the link it prints into this browser:
      </p>
      {@render commandBox('deepcove open --no-browser')}
    </div>
    <p class="border-t border-border pt-4 text-xs text-muted-foreground">
      The command only works for you, so other people on this computer stay out.
    </p>
  </div>
</div>
