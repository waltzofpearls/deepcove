<script lang="ts">
  import type { Session } from '$lib/api/types';
  import { RUNTIME_SANDBOXLESS } from '$lib/util/runtime';

  // What destroying a sandboxless session does to files on the machine. It has no sandbox, so this is
  // the one runtime where deleting a session could reach real work.
  let { session }: { session: Session } = $props();

  const sandboxless = $derived(session.runtime === RUNTIME_SANDBOXLESS);
</script>

{#if sandboxless}
  <div class="rounded-md border border-border bg-muted/40 p-3 text-sm">
    {#if session.work_dir}
      <p>deepcove never deletes a folder you picked. This one is left alone:</p>
      <p class="mt-1 font-mono text-xs">{session.work_dir}</p>
    {:else}
      <p>
        This session runs on this machine. The folder deepcove made for it is deleted too. Nothing
        else on the machine is touched.
      </p>
      {#if session.agent_work_dir}
        <p class="mt-1 font-mono text-xs">{session.agent_work_dir}</p>
      {/if}
    {/if}
  </div>
{/if}
