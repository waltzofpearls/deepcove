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
    const proxy = {
      url: 'http://100.75.35.244:9000',
      headers: ['Border0-Token'],
    };
    fetchMock.mockResolvedValue(jsonResponse(proxy));
    await expect(api.llmProxy()).resolves.toEqual(proxy);
    expect(fetchMock.mock.calls[0][0]).toBe('/api/llm-proxy');
  });

  it('saves the URL and the headers in one PUT', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        url: 'http://host:9000',
        headers: ['Border0-Token'],
      }),
    );
    const headers = [{ name: 'Border0-Token' }, { name: 'X-Team', value: 'infra' }];
    const saved = await api.saveLLMProxy('http://host:9000/', headers);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/llm-proxy');
    expect(init.method).toBe('PUT');
    expect(JSON.parse(init.body)).toEqual({
      url: 'http://host:9000/',
      headers,
    });
    expect(saved.url).toBe('http://host:9000');
  });

  it('names the header a save was refused for', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse(
        {
          error: "deepcove sets Host itself, so it can't be changed here",
          code: 'invalid_input',
          header: 'Host',
        },
        400,
      ),
    );
    await expect(api.saveLLMProxy('http://host:9000', [])).rejects.toMatchObject({
      status: 400,
      header: 'Host',
    });
  });

  it('tests with the values in the form', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        verdict: 'reached',
        result: 'answered with 200 OK',
      }),
    );
    const headers = [{ name: 'Border0-Token' }];
    await api.testLLMProxy('http://host:9000', headers);

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/llm-proxy/test');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({
      url: 'http://host:9000',
      headers,
    });
  });
});
