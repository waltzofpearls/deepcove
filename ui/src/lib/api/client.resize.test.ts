import { beforeEach, describe, expect, it, vi } from 'vitest';

const fetchMock = vi.fn();

vi.mock('./transport', () => {
  const mockTransport = {
    mode: 'direct',
    fetch: (...args: unknown[]) => fetchMock(...args),
    openSocket: vi.fn(),
  };
  return { transport: mockTransport, transportForHost: () => mockTransport };
});

import { api } from './client';

const sessionWith = (resources: object) =>
  new Response(JSON.stringify({ id: 's1', resources }), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });

beforeEach(() => {
  fetchMock.mockReset();
});

describe('resizeSessionVM', () => {
  const cases = [
    { name: 'a node that took the size', sent: { size: 'small' }, answered: { size: 'small' } },
    {
      name: 'a custom size it took',
      sent: { cpu: 2, memory_mb: 4096 },
      answered: { cpu: 2, memory_mb: 4096 },
    },
    {
      name: 'an older node that ignored it',
      sent: { size: 'small' },
      answered: { size: 'medium' },
      isRefused: true,
    },
  ];
  for (const tc of cases) {
    it(tc.name, async () => {
      fetchMock.mockResolvedValue(sessionWith(tc.answered));
      const resize = api.resizeSessionVM('s1', tc.sent);
      if (tc.isRefused) await expect(resize).rejects.toThrow("didn't change the size");
      else await expect(resize).resolves.toMatchObject({ resources: tc.answered });
    });
  }

  it('says so for a size saved for later, too', async () => {
    fetchMock.mockResolvedValue(sessionWith({ size: 'medium' }));
    await expect(api.saveSessionVMSize('s1', { size: 'small' })).rejects.toThrow(
      "didn't change the size",
    );
  });
});
