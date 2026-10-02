import { describe, it, expect } from 'vitest';
import { ApiError } from './client';
import { friendlyError, unreachableMessage } from './errors';

describe('friendlyError', () => {
  // Each known code maps to a specific title; the code + raw message pass through.
  it.each([
    { code: 'runtime_unavailable', title: "That runtime isn't available" },
    { code: 'invalid_input', title: 'Check the form and try again' },
    { code: 'conflict', title: 'That conflicts with the current state' },
    { code: 'not_found', title: 'Not found' },
    { code: 'unrecognized_code', title: 'Something went wrong' },
  ])('maps ApiError code "$code"', ({ code, title }) => {
    const fe = friendlyError(new ApiError(500, 'raw msg', code));
    expect(fe.title).toBe(title);
    expect(fe.code).toBe(code);
    expect(fe.raw).toBe('raw msg');
  });

  it('handles a plain Error (no code)', () => {
    const fe = friendlyError(new Error('boom'));
    expect(fe.title).toBe('Something went wrong');
    expect(fe.raw).toBe('boom');
    expect(fe.code).toBeUndefined();
  });

  it('handles a non-Error value', () => {
    const fe = friendlyError('nope');
    expect(fe.title).toBe('Something went wrong');
    expect(fe.raw).toBe('nope');
  });
});

describe('unreachableMessage', () => {
  it('recognises a fetch that never reached the server', () => {
    expect(
      unreachableMessage(new TypeError('NetworkError when attempting to fetch resource.')),
    ).toBe("Can't reach deepcove. Retrying...");
  });
  it('leaves other errors alone so they still surface', () => {
    expect(unreachableMessage(new Error('boom'))).toBeNull();
    expect(unreachableMessage(new ApiError(500, 'server exploded'))).toBeNull();
  });
});
