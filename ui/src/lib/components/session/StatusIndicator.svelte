<script lang="ts">
  import type { SessionStatus } from '$lib/api/types';

  let { status }: { status: SessionStatus } = $props();

  // Colours match app.css semantic tokens. Outline-pulse for transient
  // states (reconnecting, errored), solid for stable ones.
  const cls = $derived.by(() => {
    switch (status) {
      case 'running':
        return 'bg-[var(--color-success)]';
      case 'starting':
        return 'bg-[var(--color-warning)] animate-pulse';
      case 'stopped':
        return 'bg-muted-foreground';
      case 'errored':
        return 'bg-destructive';
      case 'created':
      default:
        return 'bg-[var(--color-warning)]';
    }
  });
</script>

<span class="inline-block size-2 shrink-0 rounded-full {cls}" aria-label={status}></span>
