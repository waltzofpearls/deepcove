<script lang="ts">
  // The Settings card for one agent's login, with a row for each of your nodes.
  import AgentLoginRow, { loginRowColumns } from '$lib/components/AgentLoginRow.svelte';
  import type { Agent, NodeInfo } from '$lib/api/types';

  let { agent, nodes }: { agent: Agent; nodes: NodeInfo[] } = $props();
</script>

<section class="space-y-3 rounded-xl border border-border bg-card p-4 md:col-span-2">
  <h2 class="text-sm font-medium text-muted-foreground">{agent.display_name} login</h2>
  {#if nodes.length === 0}
    <p class="text-xs text-muted-foreground">Loading…</p>
  {:else}
    <div class="hidden gap-4 text-xs text-muted-foreground sm:grid {loginRowColumns}">
      <div>Node</div>
      <div>Logged in as</div>
      <div>New sessions use</div>
      <div>API key</div>
    </div>
    {#each nodes as node (node.stable_node_id || node.hostname)}
      <AgentLoginRow {agent} {node} />
    {/each}
    <p class="border-t border-border pt-3 text-xs text-muted-foreground">
      Log in once in a session, and the other sessions on that node share it. Each session can
      change how it logs in, in its settings. An API key never enters a session.
    </p>
  {/if}
</section>
