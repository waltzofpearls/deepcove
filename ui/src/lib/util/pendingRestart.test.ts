import { describe, expect, it } from 'vitest';
import { folderAwaitingRestart, sizeAwaitingRestart } from './pendingRestart';

describe('sizeAwaitingRestart', () => {
  const sizes = [
    { name: 'medium', cpu: 1, memory_mb: 2048 },
    { name: 'large', cpu: 2, memory_mb: 4096 },
  ];
  const mediumRunning = { cpu: 1, memory_mb: 2048, disk_mb: 1024 };
  const cases = [
    { name: 'running what is saved', saved: { size: 'medium' }, want: false },
    { name: 'saved larger, not restarted', saved: { size: 'large' }, want: true },
    {
      name: 'old numbers that match',
      saved: { cpu: 1, memory_mb: 2048, disk_mb: 4096 },
      want: false,
    },
    { name: 'nothing saved', saved: undefined, want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(sizeAwaitingRestart(mediumRunning, tc.saved, sizes)).toBe(tc.want);
    });
  }
});

describe('folderAwaitingRestart', () => {
  const running = [{ host_path: '/Users/me/Dev', guest_path: '/workspace' }];
  const cases = [
    {
      name: 'sharing what is saved',
      hostRoot: '/Users/me/Dev',
      guestRoot: '/workspace',
      want: false,
    },
    {
      name: 'saved another folder',
      hostRoot: '/Users/me/Code',
      guestRoot: '/workspace',
      want: true,
    },
    {
      name: 'saved another mount point',
      hostRoot: '/Users/me/Dev',
      guestRoot: '/code',
      want: true,
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(folderAwaitingRestart(running, tc.hostRoot, tc.guestRoot)).toBe(tc.want);
    });
  }
  it('a VM with no shared folder has nothing waiting', () => {
    expect(folderAwaitingRestart(undefined, '/Users/me/Dev', '/workspace')).toBe(false);
  });
});
