import { describe, it, expect, vi, afterEach } from 'vitest';
import { api, ApiError } from './client';

function okResponse(body: unknown): Response {
  return { ok: true, status: 200, json: async () => body } as unknown as Response;
}
function errResponse(status: number, body: unknown): Response {
  return { ok: false, status, statusText: 'err', json: async () => body } as unknown as Response;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('api.fsList', () => {
  it('encodes the path into the query and returns parsed JSON', async () => {
    const fetchMock = vi.fn(async () => okResponse({ path: '/x', parent: '', entries: [] }));
    vi.stubGlobal('fetch', fetchMock);

    const res = await api.fsList('/x');

    expect(fetchMock).toHaveBeenCalledWith('/api/fs/list?path=%2Fx', { signal: undefined });
    expect(res).toEqual({ path: '/x', parent: '', entries: [] });
  });
});

describe('api.suggestName', () => {
  it('builds the URL and unwraps the name', async () => {
    const fetchMock = vi.fn(async () => okResponse({ name: 'claude-proud_shannon' }));
    vi.stubGlobal('fetch', fetchMock);

    const name = await api.suggestName('claude');

    expect(fetchMock).toHaveBeenCalledWith('/api/sessions/suggest-name?agent=claude', undefined);
    expect(name).toBe('claude-proud_shannon');
  });
});

describe('request error handling', () => {
  it('throws an ApiError carrying status, message, and code from the {error,code} envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => errResponse(409, { error: 'already running', code: 'conflict' })),
    );

    const err = await api.suggestName('x').catch((e) => e);

    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(409);
    expect(err.message).toBe('already running');
    expect(err.code).toBe('conflict');
  });
});
