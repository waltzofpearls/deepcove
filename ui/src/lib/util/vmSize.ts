import type {
  SessionResources,
  SessionVMSizeChoice,
  VMLimits,
  VMSize,
  WorkspaceVMSizeChoice,
} from '$lib/api/types';

// DEFAULT_VM_SIZE is what a VM gets unless you pick another size. The server's default is the same.
export const DEFAULT_VM_SIZE = 'medium';

// VMSizeCard is one size the picker shows. titleNote is what the picker's title adds while this size
// is picked, like "fits 6 on this machine".
export interface VMSizeCard {
  name: string;
  cpu: number;
  memoryMB: number;
  titleNote?: string;
}

export function sessionVMSizeCards(choices: SessionVMSizeChoice[]): VMSizeCard[] {
  return choices.map((choice) => ({
    name: choice.name,
    cpu: choice.cpu,
    memoryMB: choice.memory_mb,
    titleNote: fitsNote(choice.fits_on_this_host),
  }));
}

export function workspaceVMSizeCards(choices: WorkspaceVMSizeChoice[]): VMSizeCard[] {
  return choices.map((choice) => ({
    name: choice.name,
    cpu: choice.cpu,
    memoryMB: choice.memory_mb,
    titleNote: agentsNote(choice.agents_it_runs),
  }));
}

// customSessionVMTitleNote counts a custom session VM the way the server counts a size.
export function customSessionVMTitleNote(
  memoryMB: number,
  spareMemoryMB: number | undefined,
): string | undefined {
  return fitsNote(spareMemoryMB === undefined ? undefined : Math.floor(spareMemoryMB / memoryMB));
}

// customWorkspaceVMTitleNote is about one agent per GB, as the server says for a size.
export function customWorkspaceVMTitleNote(memoryMB: number): string {
  return agentsNote(Math.max(Math.floor(memoryMB / 1024), 1));
}

// fitsNote is absent when the machine's memory could not be measured: say nothing rather than guess.
function fitsNote(fits: number | undefined): string | undefined {
  if (fits === undefined) return undefined;
  // Still offered: on a small machine, a VM that might run out of memory beats none at all.
  if (fits === 0) return 'may run out of memory on this machine';
  return `fits ${fits} on this machine`;
}

function agentsNote(agents: number): string {
  return `runs about ${agents} ${agents === 1 ? 'agent' : 'agents'}`;
}

// customVMSizeLimits is the node's limits when it gave them, which the Custom sliders need.
export function customVMSizeLimits(limits: VMLimits | undefined): VMLimits | undefined {
  if (!limits || limits.max_cpu < 1 || limits.max_memory_mb < 1024) return undefined;
  return limits;
}

// isCustomVMSize reports whether resources give CPU and memory instead of a size name.
export function isCustomVMSize(resources: SessionResources): boolean {
  return !resources.size && Boolean(resources.cpu || resources.memory_mb);
}

// isPickedVMSize reports whether resources name a size or give a custom one, rather than nothing.
export function isPickedVMSize(resources: SessionResources): boolean {
  return Boolean(resources.size) || isCustomVMSize(resources);
}

// startCustomVMSize starts the sliders at the picked size's numbers, kept within what the node can give.
export function startCustomVMSize(
  card: VMSizeCard | undefined,
  limits: VMLimits,
): SessionResources {
  return {
    cpu: Math.min(card?.cpu ?? 1, limits.max_cpu),
    memory_mb: Math.min(card?.memoryMB ?? 1024, customVMMaxGB(limits) * 1024),
  };
}

// customVMMaxGB is the most memory the slider offers, in whole GB.
export function customVMMaxGB(limits: VMLimits): number {
  return Math.floor(limits.max_memory_mb / 1024);
}

// canPickVMSize reports whether a size saved elsewhere can be picked on this node: a size it has, or a
// custom size the server takes, with at least 1 CPU and 1 GB and no more than the node can give.
export function canPickVMSize(
  resources: SessionResources,
  cards: VMSizeCard[],
  limits: VMLimits | undefined,
): boolean {
  if (resources.size) return cards.some((card) => card.name === resources.size);
  const customLimits = customVMSizeLimits(limits);
  const cpu = resources.cpu ?? 0;
  const memoryMB = resources.memory_mb ?? 0;
  return (
    customLimits !== undefined &&
    cpu >= 1 &&
    cpu <= customLimits.max_cpu &&
    memoryMB >= 1024 &&
    memoryMB <= customLimits.max_memory_mb
  );
}

// A number a custom size leaves out boots at the VM's own default (internal/sandbox/vm/driver.go).
export const VM_DEFAULT_CPU = 2;
export const VM_DEFAULT_MEMORY_MB = 4096;

// changeCustomVMSize changes one number of a custom size and keeps the rest, the disk cap included. A
// number left out is kept at the default it boots with, not dropped to the smallest.
export function changeCustomVMSize(
  selected: SessionResources,
  change: { cpu: number } | { memory_mb: number },
): SessionResources {
  return {
    ...selected,
    cpu: selected.cpu || VM_DEFAULT_CPU,
    memory_mb: selected.memory_mb || VM_DEFAULT_MEMORY_MB,
    ...change,
  };
}

// sameVMSize reports whether two saved sizes are the same pick.
export function sameVMSize(
  a: SessionResources | undefined,
  b: SessionResources | undefined,
): boolean {
  return (
    (a?.size ?? '') === (b?.size ?? '') &&
    (a?.cpu ?? 0) === (b?.cpu ?? 0) &&
    (a?.memory_mb ?? 0) === (b?.memory_mb ?? 0)
  );
}

// workspaceVMSizeOrDefault keeps a picked size this machine has, else the usual default if it has that,
// else its biggest. The server picks the same way.
export function workspaceVMSizeOrDefault(cards: VMSizeCard[], picked: string): string {
  if (cards.length === 0 || cards.some((card) => card.name === picked)) return picked;
  if (cards.some((card) => card.name === DEFAULT_VM_SIZE)) return DEFAULT_VM_SIZE;
  return cards[cards.length - 1].name;
}

// formatGB shows memory the way the size cards do, like "2 GB" or "1.5 GB".
export function formatGB(memoryMB: number): string {
  return `${Number((memoryMB / 1024).toFixed(1))} GB`;
}

// runsAtSize reports whether a VM is running with this size's CPU and memory.
export function runsAtSize(running: SessionResources, size: VMSize): boolean {
  return running.cpu === size.cpu && running.memory_mb === size.memory_mb;
}

// formatRunningSize shows the CPU and memory a VM is running with, like "4 CPU · 8 GB".
export function formatRunningSize(running: SessionResources): string {
  const parts: string[] = [];
  if (running.cpu) parts.push(`${running.cpu} CPU`);
  if (running.memory_mb) parts.push(formatGB(running.memory_mb));
  return parts.join(' · ');
}

// describeVMSize names a saved size with what it gives, like "Medium · 1 CPU · 2 GB". Numbers with no
// name read as CPU and memory alone, and a name whose numbers are not known yet reads as the name.
export function describeVMSize(
  resources: SessionResources | undefined,
  sizes: VMSize[] | undefined,
): string {
  const size = sizes?.find((choice) => choice.name === resources?.size);
  if (!size) {
    const numbers = resources ? formatRunningSize(resources) : '';
    return numbers || capitalize(resources?.size ?? '');
  }
  return `${capitalize(size.name)} · ${size.cpu} CPU · ${formatGB(size.memory_mb)}`;
}

function capitalize(word: string): string {
  return word.charAt(0).toUpperCase() + word.slice(1);
}
