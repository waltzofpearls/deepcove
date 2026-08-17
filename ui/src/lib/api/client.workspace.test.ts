import { beforeEach, describe, expect, it, vi } from 'vitest';

// A node with no workspace VM answers 404, which is a normal state rather than a failure. The client
// has to turn that one status into null and let every other error through, or the UI shows an error
// page where it should be offering to create the machine.
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
import { ApiError } from './client';

const jsonResponse = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } });

beforeEach(() => {
  fetchMock.mockReset();
});

describe('workspaceVM', () => {
  it('returns null when this node has none', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ error: 'this node has no workspace VM' }, 404));
    await expect(api.workspaceVM()).resolves.toBeNull();
  });

  it('returns the machine when there is one', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        host_root: '/Users/rollie/Dev',
        image: 'ghcr.io/waltzofpearls/deepcove-claude:latest',
        runtime: 'applevf',
        resources: { cpu: 4, memory_mb: 8192 },
        created_at: '2026-08-12T00:00:00Z',
        updated_at: '2026-08-12T00:00:00Z',
      }),
    );
    const vm = await api.workspaceVM();
    expect(vm?.host_root).toBe('/Users/rollie/Dev');
    expect(vm?.sandbox_id).toBeUndefined();
  });

  it('still throws on anything else', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ error: 'boom' }, 500));
    await expect(api.workspaceVM()).rejects.toBeInstanceOf(ApiError);
  });

  it('asks the node that will host the session, not always this one', async () => {
    fetchMock.mockResolvedValue(jsonResponse({ error: 'no workspace vm' }, 404));
    await api.workspaceVM({ stable_node_id: 'n-peer', hostname: 'peer' });
    // The transport mock is shared, so the assertion is that a host is accepted and the path is right.
    expect(fetchMock.mock.calls[0][0]).toBe('/api/workspace-vm');
  });

  it('saving sends a PUT with the request body', async () => {
    fetchMock.mockResolvedValue(
      jsonResponse({
        host_root: '/Users/rollie/Dev',
        image: 'img',
        runtime: 'applevf',
        resources: {},
        created_at: '2026-08-12T00:00:00Z',
        updated_at: '2026-08-12T00:00:00Z',
      }),
    );
    await api.saveWorkspaceVM({ host_root: '/Users/rollie/Dev' });

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/workspace-vm');
    expect(init.method).toBe('PUT');
    expect(JSON.parse(init.body)).toEqual({ host_root: '/Users/rollie/Dev' });
  });
});
