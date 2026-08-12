<script lang="ts">
  import type { SessionStatus } from '$lib/api/types';
  import { sessions } from '$lib/stores/sessions.svelte';

  let { status }: { status: SessionStatus } = $props();

  // Colours match app.css semantic tokens. Outline-pulse for transient
  // states (reconnecting, errored), solid for stable ones.
  const cls = $derived.by(() => {
    // Unreachable node: every status here is the last one we saw. A green dot next to "can't reach
    // deepcove" reads as if the session is fine.
    if (!sessions.reachable) return 'bg-muted-foreground';
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

<span
  class="inline-block size-2 shrink-0 rounded-full {cls}"
  aria-label={sessions.reachable ? status : 'unknown'}
></span>
