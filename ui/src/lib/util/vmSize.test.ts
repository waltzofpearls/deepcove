import { describe, expect, it } from 'vitest';
import {
  describeVMSize,
  formatGB,
  runsAtSize,
  sessionVMSizeCards,
  workspaceVMSizeCards,
  workspaceVMSizeOrDefault,
} from './vmSize';

describe('sessionVMSizeCards', () => {
  const medium = { name: 'medium', cpu: 1, memory_mb: 2048 };
  const cases = [
    {
      name: 'says how many fit',
      choice: { ...medium, fits_on_this_host: 6 },
      want: 'fits 6 on this machine',
    },
    {
      name: 'warns about a size that does not fit, and still offers it',
      choice: { ...medium, fits_on_this_host: 0 },
      want: 'may run out of memory on this machine',
    },
    {
      name: 'says nothing when the machine could not be measured',
      choice: medium,
      want: undefined,
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      const [card] = sessionVMSizeCards([tc.choice]);
      expect(card.titleNote).toBe(tc.want);
    });
  }
});

describe('workspaceVMSizeCards', () => {
  const cases = [
    { name: 'several agents', agents: 3, want: 'runs about 3 agents' },
    { name: 'one agent', agents: 1, want: 'runs about 1 agent' },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      const [card] = workspaceVMSizeCards([
        { name: 'small', cpu: 1, memory_mb: 1024, agents_it_runs: tc.agents },
      ]);
      expect(card.titleNote).toBe(tc.want);
    });
  }
});

describe('workspaceVMSizeOrDefault', () => {
  const card = (name: string) => ({ name, cpu: 1, memoryMB: 2048 });
  const cases = [
    {
      name: 'keeps a size the machine has',
      cards: [card('small'), card('large')],
      picked: 'small',
      want: 'small',
    },
    {
      name: 'falls back to medium',
      cards: [card('small'), card('medium')],
      picked: 'large',
      want: 'medium',
    },
    { name: 'else the biggest', cards: [card('large')], picked: 'medium', want: 'large' },
    { name: 'leaves it alone with no sizes', cards: [], picked: 'medium', want: 'medium' },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(workspaceVMSizeOrDefault(tc.cards, tc.picked)).toBe(tc.want);
    });
  }
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
    {
      name: 'a size whose numbers are not known yet',
      resources: { size: 'large' },
      want: 'Large',
    },
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
