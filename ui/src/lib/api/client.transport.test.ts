import { beforeEach, describe, expect, it, vi } from 'vitest';

// Prove the API client funnels every call through the transport seam (so the
// server-injected mode actually governs real traffic), rather than calling
// global fetch / new WebSocket directly. We mock the transport module and
// assert the client routes through it. If client.ts ever reverts to raw
// fetch/WebSocket, these go red.
const fetchMock = vi.fn();
const openSocketMock = vi.fn();

vi.mock('./transport', () => {
  const mockTransport = {
    mode: 'direct',
    fetch: (...args: unknown[]) => fetchMock(...args),
    openSocket: (...args: unknown[]) => openSocketMock(...args),
  };
  return {
    transport: mockTransport,
    // Session-scoped calls route through transportForHost; with no host it resolves to the boot
    // transport, so the mock returns the same object the rest of these assertions watch.
    transportForHost: () => mockTransport,
  };
});

import { api } from './client';

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'content-type': 'application/json' },
  });

beforeEach(() => {
  fetchMock.mockReset();
  openSocketMock.mockReset();
});

describe('api client routes through the transport seam', () => {
  it('GET health() goes through transport.fetch, not global fetch', async () => {
    fetchMock.mockResolvedValue(json({ ok: true, version: '1.2.3' }));
    const res = await api.health();
    expect(fetchMock).toHaveBeenCalledWith('/api/health', undefined);
    expect(res.version).toBe('1.2.3');
  });

  it('DELETE destroySession() routes its method through transport.fetch', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }));
    await api.destroySession('sess-1');
    expect(fetchMock).toHaveBeenCalledWith('/api/sessions/sess-1', { method: 'DELETE' });
  });

  it('me() returns the person, or null when the node knows no one', async () => {
    fetchMock.mockResolvedValueOnce(json({ login_name: 'ana@example.com', display_name: 'Ana' }));
    expect((await api.me())?.login_name).toBe('ana@example.com');
    expect(fetchMock).toHaveBeenCalledWith('/api/me');
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 404 }));
    expect(await api.me()).toBeNull();
  });

  it('myProfilePicture() fetches the picture through the node and returns a data: URL', async () => {
    fetchMock.mockResolvedValue(
      new Response(new Uint8Array([0x89, 0x50, 0x4e, 0x47]), {
        status: 200,
        headers: { 'content-type': 'image/png' },
      }),
    );
    expect(await api.myProfilePicture()).toBe('data:image/png;base64,iVBORw==');
    expect(fetchMock).toHaveBeenCalledWith('/api/me/profile-picture');
  });

  it('myProfilePicture() returns null when there is no picture', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 404 }));
    expect(await api.myProfilePicture()).toBeNull();
  });

  it('openAttachSocket() opens via transport.openSocket with the pty subprotocol', () => {
    openSocketMock.mockReturnValue({});
    api.openAttachSocket('sess-1', 'win-2');
    expect(openSocketMock).toHaveBeenCalledWith(
      '/api/sessions/sess-1/attach?tmux_window=win-2',
      'deepcove.pty.v1',
    );
  });
});
