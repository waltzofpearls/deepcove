<script lang="ts">
  import type { Session } from '$lib/api/types';
  import { ageOf, shortId } from '$lib/format';
  import StatusDot from './StatusDot.svelte';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';

  let { sessions }: { sessions: Session[] } = $props();
</script>

<div class="overflow-hidden border-b border-border">
  <table class="w-full text-sm">
    <thead class="bg-card text-xs text-muted-foreground">
      <tr>
        <th class="px-4 py-2 text-left font-medium"></th>
        <th class="px-4 py-2 text-left font-medium">Name</th>
        <th class="px-4 py-2 text-left font-medium">Agent</th>
        <th class="px-4 py-2 text-left font-medium">ID</th>
        <th class="px-4 py-2 text-left font-medium">Active</th>
        <th class="px-4 py-2"></th>
      </tr>
    </thead>
    <tbody>
      {#each sessions as session (session.id)}
        <tr class="border-t border-border hover:bg-secondary">
          <td class="px-4 py-3">
            <StatusDot status={session.status} />
          </td>
          <td class="px-4 py-3 font-mono">{session.name}</td>
          <td class="px-4 py-3">
            <span class="rounded bg-secondary px-2 py-0.5 text-xs">{session.agent}</span>
          </td>
          <td class="px-4 py-3 font-mono text-xs text-muted-foreground">{shortId(session.id)}</td>
          <td class="px-4 py-3 text-xs text-muted-foreground">{ageOf(session.last_activity_at)}</td>
          <td class="px-4 py-3 text-right">
            <a
              href="/sessions/{session.id}"
              class="inline-flex items-center text-muted-foreground hover:text-foreground"
            >
              <ChevronRight class="size-4" />
            </a>
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
