import { describe, expect, it } from 'vitest';
import {
  describeVMSize,
  formatGB,
  runsAtSize,
  sessionVMSizeCards,
  workspaceVMSizeCards,
} from './vmSize';

describe('sessionVMSizeCards', () => {
  const medium = { name: 'medium', cpu: 1, memory_mb: 2048 };
  const cases = [
    {
      name: 'says how many fit',
      choice: { ...medium, fits_on_this_host: 6 },
      want: { titleNote: 'fits 6 on this machine', tooBigForThisMachine: undefined },
    },
    {
      name: 'greys out a size that does not fit at all',
      choice: { ...medium, fits_on_this_host: 0 },
      want: { titleNote: 'too big for this machine', tooBigForThisMachine: true },
    },
    {
      name: 'says nothing when the machine could not be measured',
      choice: medium,
      want: { titleNote: undefined, tooBigForThisMachine: undefined },
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      const [card] = sessionVMSizeCards([tc.choice]);
      expect(card.titleNote).toBe(tc.want.titleNote);
      expect(card.tooBigForThisMachine).toBe(tc.want.tooBigForThisMachine);
    });
  }
});

describe('workspaceVMSizeCards', () => {
  it('says about how many agents it runs', () => {
    const [card] = workspaceVMSizeCards([
      { name: 'small', cpu: 2, memory_mb: 4096, agents_it_runs: 3 },
    ]);
    expect(card.titleNote).toBe('runs about 3 agents');
  });
});

describe('runsAtSize', () => {
  const medium = { name: 'medium', cpu: 4, memory_mb: 8192 };
  it('matches CPU and memory, whatever the disk', () => {
    expect(runsAtSize({ cpu: 4, memory_mb: 8192, disk_mb: 4096 }, medium)).toBe(true);
  });
  it('a different memory is a different size', () => {
    expect(runsAtSize({ cpu: 4, memory_mb: 4096 }, medium)).toBe(false);
  });
});

describe('describeVMSize', () => {
  const sizes = [{ name: 'medium', cpu: 1, memory_mb: 2048 }];
  const cases = [
    {
      name: 'a known size with its numbers',
      resources: { size: 'medium' },
      want: 'Medium · 1 CPU · 2 GB',
    },
    {
      name: 'numbers with no name, without the disk',
      resources: { cpu: 2, memory_mb: 4096, disk_mb: 10240 },
      want: '2 CPU · 4 GB',
    },
    { name: 'nothing saved', resources: undefined, want: '' },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(describeVMSize(tc.resources, sizes)).toBe(tc.want);
    });
  }
});

describe('formatGB', () => {
  it.each([
    [2048, '2 GB'],
    [1536, '1.5 GB'],
    [1000, '1 GB'],
  ])('%d MB is %s', (memoryMB, want) => {
    expect(formatGB(memoryMB)).toBe(want);
  });
});
