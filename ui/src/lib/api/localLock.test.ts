import { afterEach, describe, expect, it, vi } from 'vitest';
import { checkHealth } from './localLock';

describe('checkHealth', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const cases = [
    { name: 'a locked browser', answer: () => new Response(null, { status: 401 }), want: 'locked' },
    {
      name: 'an unlocked browser',
      answer: () => new Response('{}', { status: 200 }),
      want: 'open',
    },
    {
      name: 'another node refusing',
      answer: () => new Response(null, { status: 403 }),
      want: 'down',
    },
    {
      name: 'deepcove restarting',
      answer: () => new Response(null, { status: 502 }),
      want: 'down',
    },
    {
      name: 'deepcove is down',
      answer: () => {
        throw new TypeError('Failed to fetch');
      },
      want: 'down',
    },
  ];
  for (const tc of cases) {
    it(tc.name, async () => {
      vi.stubGlobal(
        'fetch',
        vi.fn(async () => tc.answer()),
      );
      expect(await checkHealth()).toBe(tc.want);
    });
  }

  it('deepcove takes the call and never answers', async () => {
    // The timeout has already run out, so the stalled call ends at once.
    vi.spyOn(AbortSignal, 'timeout').mockReturnValue(AbortSignal.abort());
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise((_resolve, reject) => {
            if (init.signal?.aborted) reject(init.signal.reason);
            init.signal?.addEventListener('abort', () => reject(init.signal?.reason));
          }),
      ),
    );
    expect(await checkHealth()).toBe('down');
    vi.restoreAllMocks();
  });
});
