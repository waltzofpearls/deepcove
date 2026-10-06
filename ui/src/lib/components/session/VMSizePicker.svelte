<script lang="ts">
  import { Label } from '$lib/components/ui/label';
  import { formatGB, type VMSizeCard } from '$lib/util/vmSize';

  let {
    title,
    cards,
    selected,
    onselect,
  }: {
    title: string;
    cards: VMSizeCard[];
    selected: string;
    onselect: (name: string) => void;
  } = $props();

  const selectedCard = $derived(cards.find((card) => card.name === selected));
  // Ties the radios into one group, so the browser gives them arrow keys. Unique per picker on the page.
  const groupName = $props.id();
</script>

<div class="space-y-2">
  <Label>
    {title}
    {#if selectedCard?.titleNote}
      <span class="font-normal text-muted-foreground">· {selectedCard.titleNote}</span>
    {/if}
  </Label>
  <div class="grid grid-cols-3 gap-2" role="radiogroup" aria-label={title}>
    {#each cards as card (card.name)}
      <label
        class="cursor-pointer rounded-md border p-3 text-left text-sm transition-colors hover:bg-accent has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-50 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring"
        class:border-primary={card.name === selected}
        class:bg-accent={card.name === selected}
      >
        <input
          type="radio"
          class="sr-only"
          name={groupName}
          value={card.name}
          checked={card.name === selected}
          disabled={card.tooBigForThisMachine}
          onchange={() => onselect(card.name)}
        />
        <div class="font-medium capitalize">{card.name}</div>
        <div class="text-xs text-muted-foreground">{card.cpu} CPU</div>
        <div class="text-xs text-muted-foreground">{formatGB(card.memoryMB)} memory</div>
      </label>
    {/each}
  </div>
</div>
