// Turns the resource inputs of the create form into the wire shape. The form works in cores and GiB
// because that is how people think about a machine's size; the API works in cores and MiB. Keeping the
// conversion here, rather than inline in the dialog, is what makes it testable.

import type { SessionResources } from '$lib/api/types';

// ResourceInput is what a bound number input actually hands back, which is NOT a string: Svelte coerces
// `bind:value` on `<input type="number">` to a number, and to null once the field is cleared. Typing the
// state as string and trusting it is how this blew up with "r.trim is not a function".
export type ResourceInput = string | number | null | undefined;

// ResourceFields is the raw form state. Blank has to stay distinguishable from zero: blank means the
// runtime's default, zero would mean a real cap of nothing.
export interface ResourceFields {
  cpu: ResourceInput;
  memoryGiB: ResourceInput;
  diskGiB: ResourceInput;
}

export const emptyResourceFields: ResourceFields = { cpu: '', memoryGiB: '', diskGiB: '' };

// ParsedResources is either a usable payload or the first thing wrong with the form. A blank field is
// not wrong: it means let the runtime decide, which is what every session got before these inputs
// existed.
export interface ParsedResources {
  // resources is undefined when every field is blank, so the create request omits the key entirely.
  resources?: SessionResources;
  error?: string;
}

// Upper bounds are sanity rails, not policy. They exist so a mistyped 9999 asks a question here instead
// of failing deep inside a hypervisor that was asked for ten terabytes of RAM.
const maxCPU = 64;
const maxGiB = 512;

export function parseResources(fields: ResourceFields, includeDisk: boolean): ParsedResources {
  const cpu = parseField(fields.cpu, 'CPU', maxCPU);
  if (cpu.error) return { error: cpu.error };

  const memory = parseField(fields.memoryGiB, 'Memory', maxGiB);
  if (memory.error) return { error: memory.error };

  // The disk field is hidden for runtimes that ignore it, so anything typed before switching runtime
  // must be dropped rather than sent: the user can no longer see it to correct it.
  const disk = includeDisk ? parseField(fields.diskGiB, 'Disk', maxGiB) : {};
  if (disk.error) return { error: disk.error };

  const resources: SessionResources = {};
  if (cpu.value !== undefined) resources.cpu = cpu.value;
  if (memory.value !== undefined) resources.memory_mb = gibToMiB(memory.value);
  if (disk.value !== undefined) resources.disk_mb = gibToMiB(disk.value);

  if (Object.keys(resources).length === 0) return {};
  return { resources };
}

function parseField(
  raw: ResourceInput,
  label: string,
  max: number,
): { value?: number; error?: string } {
  // Normalize whatever the input handed back into text before deciding anything. `?? ''` matters:
  // String(null) is "null", which would parse as a bad number rather than as an empty field.
  const text = String(raw ?? '').trim();
  if (text === '') return {};
  const value = Number(text);
  if (!Number.isFinite(value)) return { error: `${label} must be a number.` };
  if (value <= 0) return { error: `${label} must be greater than zero.` };
  if (value > max) return { error: `${label} cannot be more than ${max}.` };
  return { value };
}

// Rounds up, so a fractional GiB never becomes a smaller cap than the one that was asked for.
function gibToMiB(gib: number): number {
  return Math.ceil(gib * 1024);
}

// formatResources renders a size for display, e.g. "2 CPU · 2 GiB · 256 MiB". Zero fields are omitted
// rather than shown as 0, since zero means the runtime chose. When nothing is set at all the caller gets
// an empty string and decides what to say, because "the driver applied its defaults" and "the driver
// cannot tell us" read the same here but mean different things.
export function formatResources(r: SessionResources | undefined): string {
  if (!r) return '';
  const parts: string[] = [];
  if (r.cpu) parts.push(`${trimFloat(r.cpu)} CPU`);
  if (r.memory_mb) parts.push(formatMiB(r.memory_mb));
  if (r.disk_mb) parts.push(`${formatMiB(r.disk_mb)} disk`);
  return parts.join(' · ');
}

// formatMiB shows GiB once a value is a whole number of them, which is how the sizes people actually
// pick (1, 2, 8) end up written. Anything else stays in MiB rather than becoming "1.33 GiB".
export function formatMiB(mib: number): string {
  if (mib >= 1024 && mib % 1024 === 0) return `${mib / 1024} GiB`;
  return `${trimFloat(mib)} MiB`;
}

// trimFloat drops a trailing ".0" so 2 renders as "2" and 1.5 survives as "1.5".
function trimFloat(n: number): string {
  return Number.isInteger(n) ? String(n) : String(Number(n.toFixed(2)));
}

// fieldsFromDefaults seeds the form with a runtime's real defaults, in each field's own unit: the memory
// and disk inputs are GiB, the wire is MiB. Filling the inputs beats a placeholder because the numbers
// are then visible AND editable, instead of being something you have to clear a field to discover.
//
// A dimension the runtime does not cap comes back blank, which is the honest answer for a backend that
// imposes no limit of its own. Blank still means "let the runtime decide" on submit.
export function fieldsFromDefaults(defaults: SessionResources | undefined): ResourceFields {
  if (!defaults) return { ...emptyResourceFields };
  return {
    cpu: defaults.cpu ? trimFloat(defaults.cpu) : '',
    memoryGiB: mibToGiB(defaults.memory_mb),
    diskGiB: mibToGiB(defaults.disk_mb),
  };
}

function mibToGiB(mib: number | undefined): string {
  if (!mib) return '';
  return trimFloat(mib / 1024);
}
