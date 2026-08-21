import { beforeEach, describe, expect, it, vi } from 'vitest';

// The proxy setting is what keeps the provider key out of the sandbox, so the client has to send
// exactly what the user typed to the node serving the page.
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

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

beforeEach(() => {
  fetchMock.mockReset();
});

describe('llmProxy', () => {
  it('reads the current proxy', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ url: 'http://100.75.35.244:9000' }));
    await expect(api.llmProxy()).resolves.toEqual({ url: 'http://100.75.35.244:9000' });
    expect(fetchMock.mock.calls[0][0]).toBe('/api/llm-proxy');
  });

  it('saves with a PUT and returns what the server normalized', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ url: 'http://host:9000' }));
    const saved = await api.saveLLMProxy('http://host:9000/');

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/llm-proxy');
    expect(init.method).toBe('PUT');
    expect(JSON.parse(init.body)).toEqual({ url: 'http://host:9000/' });
    expect(saved.url).toBe('http://host:9000');
  });

  it('clears by saving an empty url', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ url: '' }));
    await api.saveLLMProxy('');
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ url: '' });
  });
});
