<script lang="ts">
  import type { SessionResources, VMLimits } from '$lib/api/types';
  import { Label } from '$lib/components/ui/label';
  import {
    changeCustomVMSize,
    customVMMaxGB,
    customVMSizeLimits,
    formatGB,
    isCustomVMSize,
    startCustomVMSize,
    VM_DEFAULT_CPU,
    VM_DEFAULT_MEMORY_MB,
    type VMSizeCard,
  } from '$lib/util/vmSize';

  let {
    title,
    cards,
    selected,
    onselect,
    limits,
    customTitleNote,
  }: {
    title: string;
    cards: VMSizeCard[];
    selected: SessionResources;
    onselect: (resources: SessionResources) => void;
    // The most one VM can have on the node. Custom shows only when the node gave it.
    limits?: VMLimits;
    customTitleNote?: (memoryMB: number) => string | undefined;
  } = $props();

  const customLimits = $derived(customVMSizeLimits(limits));
  const isCustom = $derived(isCustomVMSize(selected));
  const selectedCard = $derived(
    isCustom ? undefined : cards.find((card) => card.name === selected.size),
  );
  const cpu = $derived(selected.cpu || VM_DEFAULT_CPU);
  const memoryMB = $derived(selected.memory_mb || VM_DEFAULT_MEMORY_MB);
  // The slider moves in whole GB. The labels show the exact memory, which the API can set to any MB.
  const memoryGB = $derived(Math.round(memoryMB / 1024));
  const titleNote = $derived(isCustom ? customTitleNote?.(memoryMB) : selectedCard?.titleNote);
  // Ties the radios into one group, so the browser gives them arrow keys. Unique per picker on the page.
  const groupName = $props.id();
</script>

<div class="space-y-2">
  <Label>
    {title}
    {#if titleNote}
      <span class="font-normal text-muted-foreground">· {titleNote}</span>
    {/if}
  </Label>
  <div class="grid grid-cols-3 gap-2" role="radiogroup" aria-label={title}>
    {#each cards as card (card.name)}
      <label
        class="cursor-pointer rounded-md border p-3 text-left text-sm transition-colors hover:bg-accent has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring"
        class:border-primary={card === selectedCard}
        class:bg-accent={card === selectedCard}
      >
        <input
          type="radio"
          class="sr-only"
          name={groupName}
          value={card.name}
          checked={card === selectedCard}
          onchange={() => onselect({ size: card.name })}
        />
        <div class="font-medium capitalize">{card.name}</div>
        <div class="text-xs text-muted-foreground">{card.cpu} CPU</div>
        <div class="text-xs text-muted-foreground">{formatGB(card.memoryMB)} memory</div>
      </label>
    {/each}
    {#if customLimits}
      <label
        class="cursor-pointer rounded-md border p-3 text-left text-sm transition-colors hover:bg-accent has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring"
        class:border-primary={isCustom}
        class:bg-accent={isCustom}
      >
        <input
          type="radio"
          class="sr-only"
          name={groupName}
          value="custom"
          checked={isCustom}
          onchange={() => onselect(startCustomVMSize(selectedCard, customLimits))}
        />
        <div class="font-medium">Custom</div>
        {#if isCustom}
          <div class="text-xs text-muted-foreground">{cpu} CPU</div>
          <div class="text-xs text-muted-foreground">{formatGB(memoryMB)} memory</div>
        {:else}
          <div class="text-xs text-muted-foreground">Pick CPU and memory</div>
        {/if}
      </label>
    {/if}
  </div>
  {#if isCustom && customLimits}
    <div class="space-y-4 pt-2">
      <div class="space-y-2">
        <div class="flex justify-between text-sm">
          <Label for="{groupName}-cpu">CPU</Label>
          <span>{cpu}</span>
        </div>
        <input
          id="{groupName}-cpu"
          type="range"
          class="w-full accent-primary"
          min="1"
          max={customLimits.max_cpu}
          step="1"
          value={cpu}
          oninput={(event) =>
            onselect(changeCustomVMSize(selected, { cpu: event.currentTarget.valueAsNumber }))}
        />
        <div class="flex justify-between text-xs text-muted-foreground">
          <span>1</span>
          <span>{customLimits.max_cpu}, the most this node can give</span>
        </div>
      </div>
      <div class="space-y-2">
        <div class="flex justify-between text-sm">
          <Label for="{groupName}-memory">Memory</Label>
          <span>{formatGB(memoryMB)}</span>
        </div>
        <input
          id="{groupName}-memory"
          type="range"
          class="w-full accent-primary"
          min="1"
          max={customVMMaxGB(customLimits)}
          step="1"
          value={memoryGB}
          oninput={(event) =>
            onselect(
              changeCustomVMSize(selected, {
                memory_mb: event.currentTarget.valueAsNumber * 1024,
              }),
            )}
        />
        <div class="flex justify-between text-xs text-muted-foreground">
          <span>1 GB</span>
          <span>{customVMMaxGB(customLimits)} GB, the most this node can give</span>
        </div>
      </div>
    </div>
  {/if}
</div>
