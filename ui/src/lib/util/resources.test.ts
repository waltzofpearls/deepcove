import { describe, expect, it } from 'vitest';
import {
  emptyResourceFields,
  fieldsFromDefaults,
  formatMiB,
  formatResources,
  parseResources,
  type ResourceFields,
} from './resources';

function fields(over: Partial<ResourceFields> = {}): ResourceFields {
  return { ...emptyResourceFields, ...over };
}

describe('parseResources', () => {
  const cases: {
    name: string;
    fields: ResourceFields;
    includeDisk?: boolean;
    want: ReturnType<typeof parseResources>;
  }[] = [
    {
      name: 'all blank sends nothing, so the runtime keeps its defaults',
      fields: fields(),
      want: {},
    },
    {
      name: 'whitespace counts as blank',
      fields: fields({ cpu: '   ', memoryGiB: '\t' }),
      want: {},
    },
    {
      name: 'GiB become MiB',
      fields: fields({ cpu: '4', memoryGiB: '8', diskGiB: '2' }),
      want: { resources: { cpu: 4, memory_mb: 8192, disk_mb: 2048 } },
    },
    {
      name: 'a fraction of a GiB rounds up rather than down',
      fields: fields({ memoryGiB: '1.5' }),
      want: { resources: { memory_mb: 1536 } },
    },
    {
      name: 'one filled field does not force the others',
      fields: fields({ cpu: '2' }),
      want: { resources: { cpu: 2 } },
    },
    {
      name: 'disk is dropped for runtimes that ignore it',
      fields: fields({ cpu: '2', diskGiB: '4' }),
      includeDisk: false,
      want: { resources: { cpu: 2 } },
    },
    // A bound number input hands back numbers and null, never the strings this used to assume. These
    // are the shapes that actually reach the parser from the dialog.
    {
      name: 'a number from the input binding parses',
      fields: fields({ cpu: 4, memoryGiB: 1.5 }),
      want: { resources: { cpu: 4, memory_mb: 1536 } },
    },
    {
      name: 'null from a cleared number input reads as blank, not as a bad value',
      fields: fields({ cpu: null, memoryGiB: null, diskGiB: null }),
      want: {},
    },
    {
      name: 'undefined reads as blank',
      fields: fields({ cpu: undefined }),
      want: {},
    },
    {
      name: 'a numeric zero is still rejected',
      fields: fields({ cpu: 0 }),
      want: { error: 'CPU must be greater than zero.' },
    },
    {
      name: 'zero is rejected instead of silently meaning default',
      fields: fields({ cpu: '0' }),
      want: { error: 'CPU must be greater than zero.' },
    },
    {
      name: 'negatives are rejected',
      fields: fields({ memoryGiB: '-1' }),
      want: { error: 'Memory must be greater than zero.' },
    },
    {
      name: 'non-numeric input is rejected',
      fields: fields({ cpu: 'lots' }),
      want: { error: 'CPU must be a number.' },
    },
    {
      name: 'an implausible value is caught here rather than by the hypervisor',
      fields: fields({ memoryGiB: '9999' }),
      want: { error: 'Memory cannot be more than 512.' },
    },
    {
      name: 'a bad disk value is reported when the runtime uses it',
      fields: fields({ diskGiB: '0' }),
      want: { error: 'Disk must be greater than zero.' },
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(parseResources(tc.fields, tc.includeDisk ?? true)).toEqual(tc.want);
    });
  }
});

describe('formatResources', () => {
  const cases: { name: string; in: Parameters<typeof formatResources>[0]; want: string }[] = [
    { name: 'nothing set renders empty, for the caller to explain', in: {}, want: '' },
    { name: 'undefined renders empty', in: undefined, want: '' },
    {
      name: 'a full size reads left to right',
      in: { cpu: 2, memory_mb: 2048, disk_mb: 256 },
      want: '2 CPU · 2 GiB · 256 MiB disk',
    },
    {
      name: 'zero fields are omitted rather than shown as 0',
      in: { cpu: 0, memory_mb: 1024 },
      want: '1 GiB',
    },
    { name: 'a fractional CPU survives', in: { cpu: 1.5 }, want: '1.5 CPU' },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(formatResources(tc.in)).toBe(tc.want);
    });
  }
});

describe('formatMiB', () => {
  const cases: { name: string; in: number; want: string }[] = [
    { name: 'a whole number of GiB reads as GiB', in: 8192, want: '8 GiB' },
    { name: 'a size that is not whole GiB stays in MiB, not 1.33 GiB', in: 1360, want: '1360 MiB' },
    { name: 'under a GiB stays in MiB', in: 512, want: '512 MiB' },
    { name: 'exactly 1 GiB', in: 1024, want: '1 GiB' },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(formatMiB(tc.in)).toBe(tc.want);
    });
  }
});

describe('fieldsFromDefaults', () => {
  const cases: {
    name: string;
    in: Parameters<typeof fieldsFromDefaults>[0];
    want: ResourceFields;
  }[] = [
    {
      name: 'a runtime reporting no defaults leaves every field blank',
      in: undefined,
      want: { cpu: '', memoryGiB: '', diskGiB: '' },
    },
    {
      name: 'MiB become the GiB the inputs are labelled in',
      in: { cpu: 2, memory_mb: 4096, disk_mb: 2048 },
      want: { cpu: '2', memoryGiB: '4', diskGiB: '2' },
    },
    {
      name: 'a dimension the runtime does not cap stays blank rather than becoming 0',
      in: { cpu: 2 },
      want: { cpu: '2', memoryGiB: '', diskGiB: '' },
    },
    {
      name: 'a fractional GiB default survives as a fraction',
      in: { memory_mb: 1536 },
      want: { cpu: '', memoryGiB: '1.5', diskGiB: '' },
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(fieldsFromDefaults(tc.in)).toEqual(tc.want);
    });
  }

  // The seeded fields must survive a round trip, or opening the dialog and pressing Create would send
  // something different from what is on screen.
  it('round-trips back to the values it was seeded from', () => {
    const defaults = { cpu: 2, memory_mb: 4096, disk_mb: 2048 };
    expect(parseResources(fieldsFromDefaults(defaults), true)).toEqual({ resources: defaults });
  });
});
