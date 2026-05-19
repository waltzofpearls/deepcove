<script lang="ts">
  import {
    Sheet,
    SheetContent,
    SheetDescription,
    SheetHeader,
    SheetTitle,
  } from '$lib/components/ui/sheet';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { api } from '$lib/api/client';
  import type { Agent } from '$lib/api/types';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';

  // Controlled-from-parent via bind:open. v0 deliberately doesn't
  // POST /api/sessions from the browser — we surface a copy-pasteable
  // CLI command instead. Sidesteps the "which node hosts the new
  // session?" UX problem until multi-node fan-out lands.
  let { open = $bindable(false) }: { open?: boolean } = $props();

  let agents = $state<Agent[]>([]);
  let agentsError = $state<string | null>(null);
  let agent = $state('claude');
  let name = $state('');
  let copied = $state(false);

  // Lazy-load agents when the sheet opens. Avoids a request on every
  // page render and keeps the topbar light.
  $effect(() => {
    if (!open || agents.length > 0) return;
    api
      .listAgents()
      .then((a) => {
        agents = a;
        if (a.length > 0 && !a.find((x) => x.name === agent)) {
          agent = a[0].name;
        }
      })
      .catch((e) => (agentsError = String(e)));
  });

  let command = $derived.by(() => {
    let cmd = `deepcove session ${agent}`;
    if (name.trim()) cmd += ` --name ${name.trim()}`;
    return cmd;
  });

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      /* ignore */
    }
  }
</script>

<Sheet bind:open>
  <SheetContent side="right" class="sm:max-w-md">
    <SheetHeader>
      <SheetTitle>New session</SheetTitle>
      <SheetDescription>
        Run this command on the machine you want to host the session. The
        new session will appear in the dashboard once it's running.
      </SheetDescription>
    </SheetHeader>

    <div class="mt-6 space-y-4 px-4">
      <div class="space-y-2">
        <Label for="agent">Agent</Label>
        {#if agentsError}
          <div class="text-sm text-destructive">{agentsError}</div>
        {:else}
          <select
            id="agent"
            bind:value={agent}
            class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
          >
            {#each agents as a}
              <option value={a.name}>{a.name}</option>
            {/each}
          </select>
        {/if}
      </div>

      <div class="space-y-2">
        <Label for="session-name">Name (optional)</Label>
        <Input
          id="session-name"
          bind:value={name}
          placeholder="auto-generated if blank"
        />
      </div>

      <div class="space-y-2">
        <Label>Command</Label>
        <div
          class="flex items-center gap-2 rounded-md border border-border bg-secondary p-3 font-mono text-sm"
        >
          <code class="flex-1 truncate">{command}</code>
          <Button size="icon" variant="ghost" onclick={copy} aria-label="Copy command">
            {#if copied}
              <Check class="size-4 text-[var(--color-success)]" />
            {:else}
              <Copy class="size-4" />
            {/if}
          </Button>
        </div>
      </div>
    </div>
  </SheetContent>
</Sheet>
