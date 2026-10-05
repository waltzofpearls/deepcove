import type { RuntimesInfo } from '$lib/api/types';
import { describe, it, expect } from 'vitest';
import {
  RUNTIME_APPLEVF,
  RUNTIME_CLOUDHV,
  RUNTIME_SANDBOXLESS,
  bootsVMs,
  usableRuntimes,
  runtimeMissingReason,
  missingReasons,
  runtimeSetUpByDeepcove,
  runtimeSetupOffered,
  runtimeHint,
  runtimeLabel,
  runtimeSummary,
  runtimeMissingShort,
  showsRuntimePicker,
} from './runtime';

describe('runtimeLabel', () => {
  it('names each runtime the way it is talked about', () => {
    expect(runtimeLabel(RUNTIME_SANDBOXLESS)).toBe('No sandbox');
    expect(runtimeLabel(RUNTIME_APPLEVF)).toContain('Virtual machine');
    expect(runtimeLabel(RUNTIME_CLOUDHV)).toContain('Virtual machine');
  });

  // A node may offer a runtime this build predates; showing its raw name beats showing nothing.
  it('falls through to the raw name for an unknown runtime', () => {
    expect(runtimeLabel('firecracker')).toBe('firecracker');
  });
});

describe('runtimeHint', () => {
  it('explains the tradeoff for each known runtime', () => {
    expect(runtimeHint(RUNTIME_SANDBOXLESS)).not.toBe('');
    expect(runtimeHint(RUNTIME_APPLEVF)).not.toBe('');
    // The hints have to differ, or the picker gives the user nothing to choose on.
    expect(runtimeHint(RUNTIME_SANDBOXLESS)).not.toBe(runtimeHint(RUNTIME_APPLEVF));
  });

  it('is empty for an unknown runtime rather than inventing a claim', () => {
    expect(runtimeHint('firecracker')).toBe('');
  });
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
      info: { available: [RUNTIME_APPLEVF, RUNTIME_SANDBOXLESS], default: RUNTIME_APPLEVF },
      want: [RUNTIME_APPLEVF, RUNTIME_SANDBOXLESS],
    },
    {
      name: 'a down VM runtime leaves the other one',
      info: {
        available: [RUNTIME_APPLEVF, RUNTIME_SANDBOXLESS],
        default: RUNTIME_APPLEVF,
        unavailable: { [RUNTIME_APPLEVF]: "The virtual machine runtime isn't responding." },
      },
      want: [RUNTIME_SANDBOXLESS],
    },
    {
      name: 'nothing can host',
      info: {
        available: [RUNTIME_APPLEVF],
        default: RUNTIME_APPLEVF,
        unavailable: { [RUNTIME_APPLEVF]: "The virtual machine runtime isn't responding." },
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
    {
      name: 'no guest init',
      code: 'no_guest_init',
      want: 'Part of deepcove is missing. Reinstall deepcove to fix it.',
    },
    {
      name: 'no kvm says the machine cannot run VMs',
      code: 'no_kvm',
      want: "KVM is missing, or this user can't use it. If /dev/kvm exists, add this user to the kvm group, then reboot.",
    },
    {
      name: 'no tmux says the user has to install it',
      code: 'no_tmux',
      want: 'tmux is not installed, and deepcove cannot install it for you. Install it, then restart deepcove.',
    },
    {
      name: 'a runtime that would not start points at the log',
      code: 'start_failed',
      want: 'It could not start. The reason is in deepcove\'s output, or run "deepcove service status" if it runs as a service.',
    },
    {
      name: 'no host binaries',
      code: 'no_host_binaries',
      want: 'The programs it runs virtual machines with are not downloaded yet.',
    },
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

describe('runtimeSummary', () => {
  const cases: { name: string; info: RuntimesInfo | null; want: string }[] = [
    { name: 'a machine nobody has asked yet says nothing', info: null, want: '' },
    {
      name: 'what it can host',
      info: { available: ['cloudhv', 'sandboxless'], default: 'cloudhv' },
      want: 'Virtual machine · No sandbox',
    },
    {
      name: 'a runtime whose backend is down',
      info: {
        available: ['cloudhv', 'sandboxless'],
        default: 'sandboxless',
        unavailable: { cloudhv: "The virtual machine runtime isn't responding." },
      },
      want: 'Virtual machine (not responding) · No sandbox',
    },
    {
      name: 'a runtime waiting to be installed comes last',
      info: {
        available: ['sandboxless'],
        default: 'sandboxless',
        missing: { cloudhv: 'no_kernel' },
      },
      want: 'No sandbox · Virtual machine (needs setup)',
    },
    {
      name: 'a machine that cannot run VMs says why',
      info: { available: ['sandboxless'], default: '', missing: { cloudhv: 'no_kvm' } },
      want: 'No sandbox · Virtual machine (no KVM)',
    },
    {
      name: 'the status sits apart from the name, so No sandbox never reads as a verdict',
      info: {
        available: ['sandboxless'],
        default: 'sandboxless',
        unavailable: { sandboxless: 'down' },
      },
      want: 'No sandbox (not responding)',
    },
    {
      name: 'a machine with nothing to run on says so',
      info: { available: [], default: '' },
      want: 'Nothing to run sessions on',
    },
    {
      name: 'No sandbox says it needs tmux',
      info: { available: ['cloudhv'], default: 'cloudhv', missing: { sandboxless: 'no_tmux' } },
      want: 'Virtual machine · No sandbox (no tmux)',
    },
    {
      name: 'a machine whose only runtime is waiting for setup',
      info: { available: [], default: '', missing: { cloudhv: 'no_kernel' } },
      want: 'Virtual machine (needs setup)',
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(runtimeSummary(tc.info)).toBe(tc.want);
    });
  }
});

describe('showsRuntimePicker', () => {
  const cases: { name: string; options: string[]; want: boolean }[] = [
    { name: 'nothing to pick from', options: [], want: false },
    { name: 'a lone VM runtime needs no question', options: [RUNTIME_APPLEVF], want: false },
    { name: 'a lone No sandbox is always shown', options: [RUNTIME_SANDBOXLESS], want: true },
    { name: 'a real choice', options: [RUNTIME_APPLEVF, RUNTIME_SANDBOXLESS], want: true },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(showsRuntimePicker(tc.options)).toBe(tc.want);
    });
  }
});

describe('bootsVMs', () => {
  const cases: { name: string; runtime: string; want: boolean }[] = [
    { name: 'apple virtualization', runtime: RUNTIME_APPLEVF, want: true },
    { name: 'cloud hypervisor', runtime: RUNTIME_CLOUDHV, want: true },
    { name: 'no sandbox', runtime: RUNTIME_SANDBOXLESS, want: false },
    { name: 'no runtime at all', runtime: '', want: false },
    { name: 'a runtime this build does not know', runtime: 'firecracker', want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(bootsVMs(tc.runtime)).toBe(tc.want);
    });
  }
});

describe('runtimeSetUpByDeepcove', () => {
  const cases: { name: string; code: string; want: boolean }[] = [
    { name: 'a missing kernel is downloaded', code: 'no_kernel', want: true },
    { name: 'missing host binaries are downloaded', code: 'no_host_binaries', want: true },
    { name: 'tmux is left to the user', code: 'no_tmux', want: false },
    { name: 'no download gives a machine KVM', code: 'no_kvm', want: false },
    { name: 'the guest init ships inside deepcove', code: 'no_guest_init', want: false },
    { name: 'a runtime that would not start', code: 'start_failed', want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(runtimeSetUpByDeepcove(tc.code)).toBe(tc.want);
    });
  }
});

describe('runtimeSetupOffered', () => {
  const cases: { name: string; info: RuntimesInfo | null; want: boolean }[] = [
    { name: 'nothing asked yet', info: null, want: false },
    { name: 'nothing missing', info: { available: ['cloudhv'], default: 'cloudhv' }, want: false },
    {
      name: 'a VM download deepcove can do',
      info: { available: [], default: '', missing: { cloudhv: 'no_kernel' } },
      want: true,
    },
    {
      name: 'only tmux, which is left to the user',
      info: { available: [], default: '', missing: { sandboxless: 'no_tmux' } },
      want: false,
    },
    {
      name: 'tmux and a VM download together',
      info: {
        available: [],
        default: '',
        missing: { sandboxless: 'no_tmux', applevf: 'no_kernel' },
      },
      want: true,
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(runtimeSetupOffered(tc.info)).toBe(tc.want);
    });
  }
});

describe('runtimeMissingShort', () => {
  const cases: { name: string; code: string; want: string }[] = [
    { name: 'something deepcove downloads', code: 'no_kernel', want: 'needs setup' },
    { name: 'a machine without KVM', code: 'no_kvm', want: 'no KVM' },
    { name: 'a machine without tmux', code: 'no_tmux', want: 'no tmux' },
    { name: 'a build missing its guest init', code: 'no_guest_init', want: 'reinstall deepcove' },
    { name: 'a runtime that would not start', code: 'start_failed', want: 'failed to start' },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(runtimeMissingShort(tc.code)).toBe(tc.want);
    });
  }
});

describe('missingReasons', () => {
  const cases: { name: string; info: RuntimesInfo; want: string[] }[] = [
    { name: 'nothing missing', info: { available: [], default: '' }, want: [] },
    {
      name: 'a VM reason is listed',
      info: { available: [], default: '', missing: { cloudhv: 'no_kvm' } },
      want: [
        "KVM is missing, or this user can't use it. If /dev/kvm exists, add this user to the kvm group, then reboot.",
      ],
    },
    {
      name: 'No sandbox is left out',
      info: { available: [], default: '', missing: { sandboxless: 'no_tmux' } },
      want: [],
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(missingReasons(tc.info)).toEqual(tc.want);
    });
  }
});
