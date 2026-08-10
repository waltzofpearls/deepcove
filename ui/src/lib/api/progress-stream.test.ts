import { describe, expect, it } from 'vitest';
import { readProgressStream, ApiError } from './client';

// respondWith builds a Response whose body arrives in the given chunks, so a line split across a chunk
// boundary is exercised rather than assumed away.
function respondWith(chunks: string[]): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream({
    start(controller) {
      for (const c of chunks) controller.enqueue(encoder.encode(c));
      controller.close();
    },
  });
  return new Response(stream, { status: 200 });
}

describe('readProgressStream', () => {
  it('reports each progress line', async () => {
    const seen: { status: string; percent: number }[] = [];
    await readProgressStream(
      respondWith([
        '{"status":"Downloading kernel","percent":0}\n',
        '{"status":"Downloading kernel","percent":50}\n',
        '{"done":true,"percent":100}\n',
      ]),
      (p) => seen.push(p),
    );
    // The done line is a terminator, not progress.
    expect(seen).toEqual([
      { status: 'Downloading kernel', percent: 0 },
      { status: 'Downloading kernel', percent: 50 },
    ]);
  });

  it('reassembles a line split across chunks', async () => {
    const seen: { status: string; percent: number }[] = [];
    await readProgressStream(
      respondWith(['{"status":"Downloa', 'ding kernel","percent":7}\n']),
      (p) => seen.push(p),
    );
    expect(seen).toEqual([{ status: 'Downloading kernel', percent: 7 }]);
  });

  it('handles a trailing line with no newline', async () => {
    const seen: { status: string; percent: number }[] = [];
    await readProgressStream(respondWith(['{"status":"Starting","percent":-1}']), (p) =>
      seen.push(p),
    );
    expect(seen).toEqual([{ status: 'Starting', percent: -1 }]);
  });

  // The status is already 200 by the time a download fails, so without this a failure reads as success.
  it('throws on an in-band error line', async () => {
    await expect(
      readProgressStream(
        respondWith([
          '{"status":"Downloading kernel","percent":10}\n',
          '{"error":"registry unreachable"}\n',
        ]),
        () => {},
      ),
    ).rejects.toThrow(/registry unreachable/);
  });

  it('reports an in-band error as an ApiError', async () => {
    await expect(
      readProgressStream(respondWith(['{"error":"nope"}\n']), () => {}),
    ).rejects.toBeInstanceOf(ApiError);
  });

  it('treats a body-less response as done', async () => {
    const seen: unknown[] = [];
    await readProgressStream(new Response(null, { status: 200 }), (p) => seen.push(p));
    expect(seen).toEqual([]);
  });

  it('ignores blank lines', async () => {
    const seen: { status: string; percent: number }[] = [];
    await readProgressStream(respondWith(['\n', '{"status":"x","percent":1}\n', '\n']), (p) =>
      seen.push(p),
    );
    expect(seen).toEqual([{ status: 'x', percent: 1 }]);
  });
});
