import type {
  SessionResources,
  SessionVMSizeChoice,
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
  tooBigForThisMachine?: boolean;
}

export function sessionVMSizeCards(choices: SessionVMSizeChoice[]): VMSizeCard[] {
  return choices.map((choice) => {
    const card: VMSizeCard = { name: choice.name, cpu: choice.cpu, memoryMB: choice.memory_mb };
    // Absent when the machine's memory could not be measured: say nothing rather than guess.
    if (choice.fits_on_this_host === undefined) return card;
    if (choice.fits_on_this_host === 0) {
      return { ...card, titleNote: 'too big for this machine', tooBigForThisMachine: true };
    }
    return { ...card, titleNote: `fits ${choice.fits_on_this_host} on this machine` };
  });
}

export function workspaceVMSizeCards(choices: WorkspaceVMSizeChoice[]): VMSizeCard[] {
  return choices.map((choice) => ({
    name: choice.name,
    cpu: choice.cpu,
    memoryMB: choice.memory_mb,
    titleNote: `runs about ${choice.agents_it_runs} agents`,
  }));
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

// describeVMSize names a saved size with what it gives, like "Medium · 1 CPU · 2 GB". A size it does
// not know, or numbers with no name, read as CPU and memory alone.
export function describeVMSize(
  resources: SessionResources | undefined,
  sizes: VMSize[] | undefined,
): string {
  const size = sizes?.find((choice) => choice.name === resources?.size);
  if (!size) return resources ? formatRunningSize(resources) : '';
  const name = size.name.charAt(0).toUpperCase() + size.name.slice(1);
  return `${name} · ${size.cpu} CPU · ${formatGB(size.memory_mb)}`;
}
