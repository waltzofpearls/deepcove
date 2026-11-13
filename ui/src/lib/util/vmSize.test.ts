import { describe, expect, it } from 'vitest';
import {
  canPickVMSize,
  changeCustomVMSize,
  customSessionVMTitleNote,
  customVMSizeLimits,
  customWorkspaceVMTitleNote,
  describeVMSize,
  formatGB,
  isCustomVMSize,
  isPickedVMSize,
  runsAtSize,
  sameVMSize,
  sessionVMSizeCards,
  startCustomVMSize,
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

describe('customSessionVMTitleNote', () => {
  const cases = [
    {
      name: 'says how many fit',
      memoryMB: 4096,
      spareMemoryMB: 12288,
      want: 'fits 3 on this machine',
    },
    {
      name: 'warns when none fit',
      memoryMB: 16384,
      spareMemoryMB: 12288,
      want: 'may run out of memory on this machine',
    },
    {
      name: 'warns with less than 1 GB left',
      memoryMB: 1024,
      spareMemoryMB: 512,
      want: 'may run out of memory on this machine',
    },
    {
      name: 'says nothing when memory is unknown',
      memoryMB: 1024,
      spareMemoryMB: undefined,
      want: undefined,
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(customSessionVMTitleNote(tc.memoryMB, tc.spareMemoryMB)).toBe(tc.want);
    });
  }
});

describe('customWorkspaceVMTitleNote', () => {
  const cases = [
    { name: 'one agent per GB', memoryMB: 4096, want: 'runs about 4 agents' },
    { name: 'never fewer than one', memoryMB: 512, want: 'runs about 1 agent' },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(customWorkspaceVMTitleNote(tc.memoryMB)).toBe(tc.want);
    });
  }
});

describe('customVMSizeLimits', () => {
  const cases = [
    { name: 'a node that gave both', limits: { max_cpu: 4, max_memory_mb: 2048 }, want: true },
    { name: 'no limits', limits: undefined, want: false },
    { name: 'memory unknown', limits: { max_cpu: 4, max_memory_mb: 0 }, want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(customVMSizeLimits(tc.limits) !== undefined).toBe(tc.want);
    });
  }
});

describe('isCustomVMSize', () => {
  const cases = [
    { name: 'cpu and memory', resources: { cpu: 2, memory_mb: 4096 }, want: true },
    { name: 'a size name', resources: { size: 'small' }, want: false },
    { name: 'nothing', resources: {}, want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(isCustomVMSize(tc.resources)).toBe(tc.want);
    });
  }
});

describe('isPickedVMSize', () => {
  const cases = [
    { name: 'a size name', resources: { size: 'small' }, want: true },
    { name: 'a custom size', resources: { cpu: 2, memory_mb: 4096 }, want: true },
    { name: 'nothing', resources: {}, want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(isPickedVMSize(tc.resources)).toBe(tc.want);
    });
  }
});

describe('startCustomVMSize', () => {
  const limits = { max_cpu: 2, max_memory_mb: 3500 };
  const cases = [
    {
      name: 'starts at the picked size',
      card: { name: 'medium', cpu: 1, memoryMB: 2048 },
      want: { cpu: 1, memory_mb: 2048 },
    },
    {
      name: 'keeps within what the node can give, in whole GB',
      card: { name: 'medium', cpu: 4, memoryMB: 8192 },
      want: { cpu: 2, memory_mb: 3072 },
    },
    { name: 'no size picked starts small', card: undefined, want: { cpu: 1, memory_mb: 1024 } },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(startCustomVMSize(tc.card, limits)).toEqual(tc.want);
    });
  }
});

describe('sameVMSize', () => {
  const cases = [
    { name: 'same name', a: { size: 'small' }, b: { size: 'small' }, want: true },
    { name: 'other name', a: { size: 'small' }, b: { size: 'medium' }, want: false },
    {
      name: 'same numbers',
      a: { cpu: 2, memory_mb: 4096 },
      b: { cpu: 2, memory_mb: 4096 },
      want: true,
    },
    {
      name: 'other memory',
      a: { cpu: 2, memory_mb: 4096 },
      b: { cpu: 2, memory_mb: 2048 },
      want: false,
    },
    {
      name: 'a name and numbers',
      a: { size: 'small' },
      b: { cpu: 1, memory_mb: 1024 },
      want: false,
    },
    { name: 'nothing saved and nothing picked', a: {}, b: undefined, want: true },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(sameVMSize(tc.a, tc.b)).toBe(tc.want);
    });
  }
});

describe('canPickVMSize', () => {
  const cards = [{ name: 'small', cpu: 1, memoryMB: 1024 }];
  const limits = { max_cpu: 4, max_memory_mb: 8192 };
  const cases = [
    { name: 'a size this node has', resources: { size: 'small' }, limits, want: true },
    { name: 'a size it does not have', resources: { size: 'large' }, limits, want: false },
    { name: 'a custom size that fits', resources: { cpu: 4, memory_mb: 8192 }, limits, want: true },
    { name: 'cpu alone', resources: { cpu: 2 }, limits, want: false },
    { name: 'memory alone', resources: { memory_mb: 2048 }, limits, want: false },
    {
      name: 'memory the slider cannot reach but the node can give',
      resources: { cpu: 1, memory_mb: 3400 },
      limits: { max_cpu: 4, max_memory_mb: 3500 },
      want: true,
    },
    {
      name: 'a custom size too big for it',
      resources: { cpu: 8, memory_mb: 4096 },
      limits,
      want: false,
    },
    {
      name: 'a custom size where the node gave no limits',
      resources: { cpu: 1, memory_mb: 1024 },
      limits: undefined,
      want: false,
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(canPickVMSize(tc.resources, cards, tc.limits)).toBe(tc.want);
    });
  }
});

describe('changeCustomVMSize', () => {
  const cases = [
    {
      name: 'cpu alone keeps the memory it boots with',
      selected: { cpu: 2 },
      change: { cpu: 3 },
      want: { cpu: 3, memory_mb: 4096 },
    },
    {
      name: 'memory alone keeps the cpu it boots with',
      selected: { memory_mb: 2048 },
      change: { memory_mb: 3072 },
      want: { cpu: 2, memory_mb: 3072 },
    },
    {
      name: 'the disk cap stays',
      selected: { cpu: 1, memory_mb: 2048, disk_mb: 8192 },
      change: { cpu: 2 },
      want: { cpu: 2, memory_mb: 2048, disk_mb: 8192 },
    },
    {
      name: 'memory not in whole GB stays exact',
      selected: { cpu: 1, memory_mb: 1536 },
      change: { cpu: 2 },
      want: { cpu: 2, memory_mb: 1536 },
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(changeCustomVMSize(tc.selected, tc.change)).toEqual(tc.want);
    });
  }
});
