<script lang="ts">
  import { cn } from '$lib/utils';

  // SessionMode is the user-facing concept of "what am I looking at?"
  //   agent  → the tmux window where the coding agent runs (main)
  //   ssh    → a plain shell window in the same sandbox (lazy-created)
  //   split  → both side-by-side; desktop-only, hidden on mobile
  export type SessionMode = 'agent' | 'ssh' | 'split';

  let {
    mode,
    onChange,
  }: {
    mode: SessionMode;
    onChange: (next: SessionMode) => void;
  } = $props();

  // Split is hidden on mobile via Tailwind's md: prefix (handled
  // inline on the button below). The parent page also forces the
  // effective mode to 'agent' if a ?mode=split URL is opened on a
  // narrow viewport — the visual hiding here is just so the button
  // can't be tapped on a phone.
  type Opt = { value: SessionMode; label: string; mobileHidden?: boolean };
  const options: Opt[] = [
    { value: 'agent', label: 'Agent' },
    { value: 'ssh', label: 'SSH' },
    { value: 'split', label: 'Split', mobileHidden: true },
  ];
</script>

<div class="inline-flex items-center rounded-md border border-border p-0.5">
  {#each options as opt (opt.value)}
    <button
      type="button"
      aria-pressed={mode === opt.value}
      onclick={() => onChange(opt.value)}
      class={cn(
        'inline-flex items-center rounded-sm px-3 py-1 text-xs font-medium transition-colors',
        opt.mobileHidden && 'hidden md:inline-flex',
        mode === opt.value
          ? 'bg-secondary text-foreground'
          : 'text-muted-foreground hover:bg-secondary/50 hover:text-foreground'
      )}
    >
      {opt.label}
    </button>
  {/each}
</div>
