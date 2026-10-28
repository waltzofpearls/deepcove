<script lang="ts">
  import { cn } from '$lib/utils';
  import RuntimeSetupButton from '$lib/components/RuntimeSetupButton.svelte';
  import type { SessionHost } from '$lib/api/types';
  import { runtimePlaceLabel, type RuntimePlace, type RuntimeTile } from '$lib/util/runtime';
  import Shield from '@lucide/svelte/icons/shield';
  import Layers from '@lucide/svelte/icons/layers';
  import SquareTerminal from '@lucide/svelte/icons/square-terminal';

  // downloading is bindable so the dialog can hold its buttons while the VM download runs.
  let {
    tiles,
    picked,
    onpick,
    nodeName,
    host,
    onsetupdone,
    downloading = $bindable(false),
  }: {
    tiles: RuntimeTile[];
    picked: RuntimeTile | undefined;
    onpick: (place: RuntimePlace) => void;
    nodeName: string;
    host?: SessionHost;
    onsetupdone: () => void | Promise<unknown>;
    downloading?: boolean;
  } = $props();

  const tileCopy: Record<RuntimePlace, { description: string; icon: typeof Shield }> = {
    session_vm: {
      description: "A fresh VM just for this session. The agent can't touch anything else.",
      icon: Shield,
    },
    workspace_vm: {
      description: 'One VM your sessions share, each in its own folder. Starts in seconds.',
      icon: Layers,
    },
    no_sandbox: {
      description: 'Runs right on the node in tmux. Fastest, but the agent sees everything.',
      icon: SquareTerminal,
    },
  };
  const groupName = $props.id();
</script>

<div class="space-y-4">
  <div class="space-y-1">
    <h2 class="text-lg font-semibold">Which runtime?</h2>
    <p class="text-sm text-muted-foreground">
      On {nodeName}. You can't change this after the session starts.
    </p>
  </div>

  <div class="grid gap-2" role="radiogroup" aria-label="Runtime">
    {#each tiles as tile (tile.place)}
      {@const copy = tileCopy[tile.place]}
      {@const selected = tile.place === picked?.place}
      <label
        class={cn(
          'flex gap-3 rounded-lg border p-3.5 text-left transition-colors has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring',
          selected && 'border-primary bg-primary/10',
          tile.blocked ? 'cursor-not-allowed opacity-60' : 'cursor-pointer hover:bg-accent/50',
        )}
      >
        <input
          type="radio"
          class="sr-only"
          name={groupName}
          value={tile.place}
          checked={selected}
          disabled={!!tile.blocked}
          onchange={() => onpick(tile.place)}
        />
        <span
          class={cn(
            'flex size-9 shrink-0 items-center justify-center rounded-md',
            selected ? 'bg-primary/15 text-primary' : 'bg-secondary text-muted-foreground',
          )}
        >
          <copy.icon class="size-5" />
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
          <span class="text-[13px] leading-snug text-muted-foreground">{copy.description}</span>
          {#if tile.blocked}
            <span class="text-xs text-foreground">{tile.blocked}</span>
          {/if}
        </span>
      </label>
    {/each}
  </div>

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
</div>
