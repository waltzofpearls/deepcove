import type { RuntimesInfo } from '$lib/api/types';
import { describe, it, expect } from 'vitest';
import {
  RUNTIME_APPLEVF,
  RUNTIME_DOCKER,
  usableRuntimes,
  runtimeMissingReason,
  runtimeHint,
  runtimeLabel,
  runtimeShortLabel,
} from './runtime';

describe('runtimeLabel', () => {
  it('names each runtime the way it is talked about', () => {
    expect(runtimeLabel(RUNTIME_DOCKER)).toBe('Docker');
    expect(runtimeLabel(RUNTIME_APPLEVF)).toContain('Virtual machine');
  });

  // A node may offer a runtime this build predates; showing its raw name beats showing nothing.
  it('falls through to the raw name for an unknown runtime', () => {
    expect(runtimeLabel('firecracker')).toBe('firecracker');
  });
});

describe('runtimeHint', () => {
  it('explains the tradeoff for each known runtime', () => {
    expect(runtimeHint(RUNTIME_DOCKER)).not.toBe('');
    expect(runtimeHint(RUNTIME_APPLEVF)).not.toBe('');
    // The hints have to differ, or the picker gives the user nothing to choose on.
    expect(runtimeHint(RUNTIME_DOCKER)).not.toBe(runtimeHint(RUNTIME_APPLEVF));
  });

  it('is empty for an unknown runtime rather than inventing a claim', () => {
    expect(runtimeHint('firecracker')).toBe('');
  });
});

describe('runtimeShortLabel', () => {
  const cases: { name: string; in: string; want: string }[] = [
    { name: 'the VM runtime abbreviates for a tag', in: RUNTIME_APPLEVF, want: 'VM' },
    { name: 'docker keeps its name', in: RUNTIME_DOCKER, want: 'Docker' },
    {
      name: 'an unknown runtime falls through to itself rather than vanishing',
      in: 'firecracker',
      want: 'firecracker',
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(runtimeShortLabel(tc.in)).toBe(tc.want);
    });
  }
});

describe('usableRuntimes', () => {
  const cases: {
    name: string;
    info: RuntimesInfo | null;
    want: string[];
  }[] = [
    { name: 'nothing asked yet', info: null, want: [] },
    {
      name: 'every registered runtime can host',
      info: { available: [RUNTIME_DOCKER, RUNTIME_APPLEVF], default: RUNTIME_APPLEVF },
      want: [RUNTIME_DOCKER, RUNTIME_APPLEVF],
    },
    {
      name: 'a machine with no Docker still has its VM runtime',
      info: {
        available: [RUNTIME_DOCKER, RUNTIME_APPLEVF],
        default: RUNTIME_APPLEVF,
        unavailable: { [RUNTIME_DOCKER]: 'Docker is not running' },
      },
      want: [RUNTIME_APPLEVF],
    },
    {
      name: 'nothing can host',
      info: {
        available: [RUNTIME_DOCKER],
        default: RUNTIME_DOCKER,
        unavailable: { [RUNTIME_DOCKER]: 'Docker is not running' },
      },
      want: [],
    },
    {
      name: 'a node that offers no runtime at all',
      info: { available: [], default: '' },
      want: [],
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(usableRuntimes(tc.info)).toEqual(tc.want);
    });
  }
});

describe('runtimeMissingReason', () => {
  const cases: { name: string; code: string; want: string }[] = [
    {
      name: 'no kernel says what is not downloaded',
      code: 'no_kernel',
      want: 'The Linux kernel it boots sessions with is not downloaded yet.',
    },
    { name: 'no guest init', code: 'no_guest_init', want: 'Part of its setup is missing.' },
    // A newer node can report a reason this build has never seen; it must still say something true.
    {
      name: 'an unknown code still explains itself',
      code: 'something_new',
      want: 'It needs to be set up before it can run sessions.',
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(runtimeMissingReason(tc.code)).toBe(tc.want);
    });
  }
});
