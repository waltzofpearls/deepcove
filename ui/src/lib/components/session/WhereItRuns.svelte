<script lang="ts">
  import type { Snippet } from 'svelte';
  import { cn } from '$lib/utils';
  import { Label } from '$lib/components/ui/label';
  import RuntimeSetupButton from '$lib/components/RuntimeSetupButton.svelte';
  import type { NodeInfo, SessionHost } from '$lib/api/types';
  import { runtimePlaceLabel, type RuntimePlace, type RuntimeTile } from '$lib/util/runtime';
  import Shield from '@lucide/svelte/icons/shield';
  import Layers from '@lucide/svelte/icons/layers';
  import SquareTerminal from '@lucide/svelte/icons/square-terminal';

  // One row saying where the session runs, which opens to change it. It stays open while the pick
  // needs something from you, since the row would hide it. downloading is bindable so the dialog can
  // hold its buttons while the VM download runs.
  let {
    nodes,
    nodeId,
    onpicknode,
    nodeName,
    host,
    tiles,
    picked,
    onpickplace,
    isChecking,
    isFromLastSession,
    sizeSummary,
    isForcedOpen,
    isCreating,
    onsetupdone,
    downloading = $bindable(false),
    sizeControls,
    workspaceSetup,
  }: {
    nodes: NodeInfo[];
    nodeId: string;
    onpicknode: (stableNodeId: string) => void;
    nodeName: string;
    host?: SessionHost;
    tiles: RuntimeTile[];
    picked: RuntimeTile | undefined;
    onpickplace: (place: RuntimePlace) => void;
    isChecking: boolean;
    isFromLastSession: boolean;
    sizeSummary: string;
    isForcedOpen: boolean;
    // Create works on the node and place picked when it was clicked, so they hold until it is done.
    isCreating: boolean;
    onsetupdone: () => void | Promise<unknown>;
    downloading?: boolean;
    sizeControls?: Snippet;
    // The first shared VM's own fields, shown under Shared while it is picked.
    workspaceSetup?: Snippet;
  } = $props();

  const placeCopy: Record<
    RuntimePlace,
    { summary: string; description: string; icon: typeof Shield }
  > = {
    session_vm: {
      summary: 'Safest, in its own VM',
      description: "Its own VM. The agent can't touch anything else.",
      icon: Shield,
    },
    workspace_vm: {
      summary: 'Shared, in one VM with your other sessions',
      description: 'One VM your sessions share, each in its own folder. Starts in seconds.',
      icon: Layers,
    },
    no_sandbox: {
      summary: 'No isolation, right on the node',
      description: 'Runs right on the node. Fastest, but the agent sees everything.',
      icon: SquareTerminal,
    },
  };

  let isChanging = $state(false);
  const isOpen = $derived(isChanging || isForcedOpen || !picked);
  const summaryDetail = $derived(
    [sizeSummary, `on ${nodeName}`, isFromLastSession ? 'same as your last session' : '']
      .filter(Boolean)
      .join(' · '),
  );
  const groupName = $props.id();
</script>

{#if !isOpen && picked}
  {@const copy = placeCopy[picked.place]}
  <div class="flex items-center gap-3 rounded-lg border border-border bg-secondary/40 px-3.5 py-3">
    <span
      class="flex size-9 shrink-0 items-center justify-center rounded-md bg-primary/15 text-primary"
    >
      <copy.icon class="size-5" />
    </span>
    <span class="flex min-w-0 flex-1 flex-col gap-0.5">
      <span class="text-sm font-semibold">{copy.summary}</span>
      <span class="truncate text-xs text-muted-foreground">{summaryDetail}</span>
    </span>
    <button
      type="button"
      onclick={() => (isChanging = true)}
      class="px-1 py-2 text-sm text-primary underline-offset-4 hover:underline"
    >
      Change
    </button>
  </div>
{:else}
  <div class="space-y-4 rounded-lg border border-border bg-secondary/40 p-4">
    <div class="flex items-center justify-between gap-3">
      <span class="text-sm font-semibold">Where it runs</span>
      {#if isChanging && !isForcedOpen && picked}
        <button
          type="button"
          onclick={() => (isChanging = false)}
          class="p-1 text-sm text-primary underline-offset-4 hover:underline"
        >
          Done
        </button>
      {/if}
    </div>

    {#if nodes.length > 1}
      <div class="flex items-center gap-3">
        <Label for="{groupName}-node" class="w-16 shrink-0 font-normal text-muted-foreground">
          Node
        </Label>
        <select
          id="{groupName}-node"
          value={nodeId}
          onchange={(event) => onpicknode(event.currentTarget.value)}
          disabled={downloading || isCreating}
          class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
        >
          {#each nodes as node (node.stable_node_id)}
            <option value={node.stable_node_id}>
              {node.hostname}{node.self ? ' (this node)' : ''}
            </option>
          {/each}
        </select>
      </div>
    {/if}

    {#if isChecking}
      <p class="text-xs text-muted-foreground">Checking what {nodeName} can run…</p>
    {/if}

    {#if tiles.length > 0}
      <fieldset class="space-y-2">
        <legend class="mb-2 text-[13px] text-muted-foreground">How safe</legend>
        {#each tiles as tile (tile.place)}
          {@const copy = placeCopy[tile.place]}
          {@const selected = tile.place === picked?.place}
          <div
            class={cn(
              'rounded-lg border transition-colors',
              selected && 'border-primary bg-primary/10',
            )}
          >
            <label
              class={cn(
                'flex gap-3 rounded-lg p-3 text-left has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring',
                tile.blocked
                  ? 'cursor-not-allowed opacity-60'
                  : 'cursor-pointer hover:bg-accent/50',
              )}
            >
              <input
                type="radio"
                class="sr-only"
                name={groupName}
                value={tile.place}
                checked={selected}
                disabled={!!tile.blocked || downloading || isCreating}
                onchange={() => onpickplace(tile.place)}
              />
              <span
                class={cn(
                  'flex size-8 shrink-0 items-center justify-center rounded-md',
                  selected ? 'bg-primary/15 text-primary' : 'bg-secondary text-muted-foreground',
                )}
              >
                <copy.icon class="size-4" />
              </span>
              <span class="flex min-w-0 flex-col gap-1">
                <span class="flex items-center gap-2">
                  <span class="text-sm font-semibold">{runtimePlaceLabel(tile.place)}</span>
                  {#if tile.isDefault}
                    <span
                      class="rounded-full border border-primary/50 px-1.5 text-[11px] font-semibold text-primary"
                    >
                      Default
                    </span>
                  {/if}
                </span>
                <span class="text-[13px] leading-snug text-muted-foreground">
                  {copy.description}
                </span>
                {#if tile.blocked}
                  <span class="text-xs text-foreground">{tile.blocked}</span>
                {/if}
              </span>
            </label>
            {#if selected && tile.place === 'workspace_vm' && workspaceSetup}
              <div class="px-3 pb-3 pl-14">
                {@render workspaceSetup()}
              </div>
            {/if}
          </div>
        {/each}
      </fieldset>
    {/if}

    {#if picked?.needsSetup}
      <!-- The setup button's error drops to a line of its own, so it never squeezes the row. -->
      <div
        class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1.5 rounded-md border border-border px-3 py-2 [&>p[role=alert]]:mt-0 [&>p[role=alert]]:basis-full"
      >
        {#if !downloading}
          <p class="flex-1 text-xs text-muted-foreground">
            Virtual machines need a one-time download on this node.
          </p>
        {/if}
        <RuntimeSetupButton
          runtime={picked.runtime}
          {host}
          ondone={onsetupdone}
          bind:busy={downloading}
        />
      </div>
    {/if}

    {#if sizeControls}
      {@render sizeControls()}
    {/if}
  </div>
{/if}
