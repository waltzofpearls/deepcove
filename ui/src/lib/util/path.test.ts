import { describe, it, expect } from 'vitest';
import { looksLikePath, splitPath } from './path';

describe('splitPath', () => {
  it.each([
    { input: '', dir: undefined, prefix: '' },
    { input: '/Users/you/', dir: '/Users/you/', prefix: '' },
    { input: '/Users/you/Dev', dir: '/Users/you', prefix: 'Dev' },
    { input: '/x', dir: '/', prefix: 'x' },
    { input: 'rel', dir: undefined, prefix: 'rel' },
  ])('splits "$input"', ({ input, dir, prefix }) => {
    expect(splitPath(input)).toEqual({ dir, prefix });
  });
});

describe('looksLikePath', () => {
  it('accepts an absolute path', () => {
    expect(looksLikePath('/Users/rollie/Dev')).toBe(true);
    expect(looksLikePath('  /Users/rollie/Dev  ')).toBe(true);
  });

  it('rejects anything that was never a path', () => {
    expect(looksLikePath('')).toBe(false);
    expect(looksLikePath('Dev')).toBe(false);
    expect(looksLikePath('~/Dev')).toBe(false);
    expect(looksLikePath('not a path at all')).toBe(false);
  });
});
