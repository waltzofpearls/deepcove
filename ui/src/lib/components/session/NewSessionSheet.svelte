<script lang="ts">
  import { goto } from '$app/navigation';
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
  import { api, ApiError } from '$lib/api/client';
  import { sessions } from '$lib/stores/sessions.svelte';
  import type { Agent, MountRequest } from '$lib/api/types';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import Plus from '@lucide/svelte/icons/plus';
  import X from '@lucide/svelte/icons/x';

  // Controlled-from-parent via bind:open. The sheet does a direct
  // POST /api/sessions on submit (SPEC §3 + §5.7: web-UI-first
  // creation is non-negotiable). Mounts make this forced — they
  // can't realistically be piped through a copy-paste flow.
  //
  // The CLI command remains supported but is now a "for scripting"
  // affordance behind a collapsible <details>, not the primary path.
  let { open = $bindable(false) }: { open?: boolean } = $props();

  // FormMount mirrors the wire shape but uses a boolean for the
  // RO toggle so the checkbox bind:checked is straightforward.
  // Converted to MountRequest at submit time.
  type FormMount = { host_path: string; container_path: string; read_only: boolean };

  let agents = $state<Agent[]>([]);
  let agentsError = $state<string | null>(null);
  let agent = $state('claude');
  let name = $state('');
  let mounts = $state<FormMount[]>([]);

  let submitting = $state(false);
  let submitError = $state<string | null>(null);
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

  // CLI command stays reactive to the form. Quoting policy: we wrap
  // any value containing whitespace or shell metacharacters in single
  // quotes so a paste into bash/zsh works as-is. Paths with `$` or
  // backticks aren't auto-escaped beyond the single-quote wrap, which
  // is the correct shell semantics — single quotes are literal.
  const command = $derived.by(() => {
    const parts = ['deepcove', 'session', agent];
    const trimmedName = name.trim();
    if (trimmedName) parts.push('--name', shellQuote(trimmedName));
    for (const m of mounts) {
      if (!m.host_path || !m.container_path) continue;
      const seg = m.read_only
        ? `${m.host_path}:${m.container_path}:ro`
        : `${m.host_path}:${m.container_path}`;
      parts.push('--mount', shellQuote(seg));
    }
    return parts.join(' ');
  });

  function shellQuote(s: string): string {
    // Wrap in single quotes only if the value contains anything the
    // shell would otherwise interpret. Bare alphanumeric + a handful
    // of path-safe punctuation passes through unquoted.
    if (/^[A-Za-z0-9_@%+=:,./\-~]+$/.test(s)) return s;
    return `'${s.replace(/'/g, "'\\''")}'`;
  }

  function addMount() {
    mounts = [...mounts, { host_path: '', container_path: '/deepcove', read_only: false }];
  }

  function removeMount(i: number) {
    mounts = mounts.filter((_, idx) => idx !== i);
  }

  function resetForm() {
    name = '';
    mounts = [];
    submitError = null;
    submitting = false;
  }

  async function submit() {
    if (submitting) return;
    submitting = true;
    submitError = null;

    const payload = {
      agent,
      name: name.trim() || undefined,
      mounts: mounts
        // Drop incomplete rows on submit — the user might have hit
        // "add" then left a row blank. Backend would 400 anyway;
        // dropping silently here is friendlier.
        .filter((m) => m.host_path && m.container_path)
        .map<MountRequest>((m) => ({
          host_path: m.host_path,
          container_path: m.container_path,
          mode: m.read_only ? 'ro' : 'rw',
        })),
    };

    try {
      const created = await api.createSession(payload);
      sessions.add(created);
      open = false;
      resetForm();
      // Navigate to the new session detail. SvelteKit's goto
      // resolves before the next $effect tick so the destination
      // page sees the session via the store immediately.
      await goto(`/sessions/${created.id}`);
    } catch (e) {
      submitError =
        e instanceof ApiError
          ? e.message
          : e instanceof Error
            ? e.message
            : String(e);
    } finally {
      submitting = false;
    }
  }

  async function copyCommand() {
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
        Spin up a coding-agent sandbox on this node. Mount a host directory to
        give the agent access to your code.
      </SheetDescription>
    </SheetHeader>

    <div class="mt-6 flex flex-col gap-5 overflow-y-auto px-4 pb-4">
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

      <!-- Mounts. Each row is its own bordered group so visually it's
           obvious where one mount stops and the next starts. Empty
           list is fine — sessions can run without any user mounts,
           the agent just gets the scratch /workspace. -->
      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <Label>Mounts</Label>
          <Button size="sm" variant="ghost" onclick={addMount}>
            <Plus class="size-4" />
            Add mount
          </Button>
        </div>
        {#if mounts.length === 0}
          <p class="text-xs text-muted-foreground">
            No mounts. Add one to expose a host directory to the agent.
          </p>
        {/if}
        {#each mounts as m, i (i)}
          <div class="space-y-2 rounded-md border border-border p-2.5">
            <div class="space-y-2">
              <div class="space-y-1">
                <Label class="text-xs" for="mount-host-{i}">Host path</Label>
                <Input
                  id="mount-host-{i}"
                  bind:value={m.host_path}
                  placeholder="/Users/you/code/myrepo"
                />
              </div>
              <div class="space-y-1">
                <Label class="text-xs" for="mount-container-{i}">Container path</Label>
                <Input
                  id="mount-container-{i}"
                  bind:value={m.container_path}
                  placeholder="/deepcove"
                />
              </div>
            </div>
            <div class="flex items-center justify-between">
              <label class="flex cursor-pointer select-none items-center gap-1.5 text-xs">
                <input
                  type="checkbox"
                  bind:checked={m.read_only}
                  class="size-3.5 rounded border-input"
                />
                Read-only
              </label>
              <Button
                size="sm"
                variant="ghost"
                onclick={() => removeMount(i)}
                aria-label="Remove mount"
              >
                <X class="size-4" />
              </Button>
            </div>
          </div>
        {/each}
      </div>

      {#if submitError}
        <div class="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {submitError}
        </div>
      {/if}

      <div class="flex items-center justify-end gap-2">
        <Button variant="ghost" onclick={() => (open = false)} disabled={submitting}>
          Cancel
        </Button>
        <Button onclick={submit} disabled={submitting}>
          {submitting ? 'Creating…' : 'Create session'}
        </Button>
      </div>

      <!-- Power-user escape hatch: the same intent expressed as a
           CLI command. Default-collapsed; reveals on summary click.
           <details> is the right primitive here — semantic, no JS,
           no flash on toggle, native keyboard support. -->
      <details class="group rounded-md border border-border">
        <summary class="flex cursor-pointer items-center justify-between rounded-md px-3 py-2 text-sm font-medium hover:bg-secondary/50">
          <span>Or run from CLI</span>
          <span class="text-xs text-muted-foreground group-open:hidden">show</span>
          <span class="hidden text-xs text-muted-foreground group-open:inline">hide</span>
        </summary>
        <div class="border-t border-border p-3">
          <div class="flex items-start gap-2 rounded-md border border-border bg-secondary p-2.5 font-mono text-xs">
            <code class="flex-1 overflow-x-auto whitespace-pre">{command}</code>
            <Button size="icon" variant="ghost" onclick={copyCommand} aria-label="Copy command">
              {#if copied}
                <Check class="size-4 text-[var(--color-success)]" />
              {:else}
                <Copy class="size-4" />
              {/if}
            </Button>
          </div>
          <p class="mt-2 text-xs text-muted-foreground">
            Run on the machine you want to host the session. The new session
            appears in the dashboard once it's running.
          </p>
        </div>
      </details>
    </div>
  </SheetContent>
</Sheet>
