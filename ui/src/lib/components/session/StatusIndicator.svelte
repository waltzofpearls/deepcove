<script lang="ts">
  import type { Session } from '$lib/api/types';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { statusLabel } from '$lib/util/agentState';

  let { session }: { session: Pick<Session, 'status' | 'agent_state' | 'agent_state_detail'> } =
    $props();

  // Only an agent on a turn moves, so motion always means work. Starting keeps its slow fade.
  const look = $derived.by(() => {
    // Unreachable node: every status here is the last one we saw. A green dot next to "can't reach
    // deepcove" reads as if the session is fine.
    if (!sessions.reachable) return 'unknown';
    if (session.status === 'running' && session.agent_state) return session.agent_state;
    return session.status;
  });
  const label = $derived(sessions.reachable ? statusLabel(session) : 'Unknown');

  const dotClasses: Record<string, string> = {
    needs_you: 'bg-warning ring-2 ring-warning/30',
    idle: 'bg-success',
    running: 'bg-success',
    starting: 'bg-warning animate-pulse',
    created: 'bg-warning',
    error: 'bg-destructive',
    errored: 'bg-destructive',
  };
</script>

{#if look === 'working'}
  <span
    class="inline-block size-2.5 shrink-0 animate-spin rounded-full border-2 border-success/25 border-t-success motion-reduce:animate-none"
    role="img"
    aria-label={label}
    title={label}
  ></span>
{:else if look === 'done'}
  <span
    class="inline-flex size-3 shrink-0 items-center justify-center rounded-full bg-success text-background"
    role="img"
    aria-label={label}
    title={label}
  >
    <svg viewBox="0 0 12 12" class="size-2.5" aria-hidden="true">
      <path
        d="M3 6.2 5 8.2 9 4"
        fill="none"
        stroke="currentColor"
        stroke-width="1.6"
        stroke-linecap="round"
        stroke-linejoin="round"
      />
    </svg>
  </span>
{:else}
  <span
    class="inline-block size-2 shrink-0 rounded-full {dotClasses[look] ?? 'bg-muted-foreground'}"
    role="img"
    aria-label={label}
    title={label}
  ></span>
{/if}
