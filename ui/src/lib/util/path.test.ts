import { describe, it, expect } from 'vitest';
import { looksLikePath, pathInsideFolder, splitPath, withoutTrailingSlash } from './path';

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

describe('pathInsideFolder', () => {
  const cases = [
    { name: 'the folder itself', path: '/Users/me/Dev/', want: '' },
    { name: 'a folder inside', path: '/Users/me/Dev/app', want: 'app' },
    { name: 'deeper inside', path: '/Users/me/Dev/work/app/', want: 'work/app' },
    { name: 'outside', path: '/Users/me/', want: null },
    { name: 'a name that only starts the same', path: '/Users/me/Devtools', want: null },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(pathInsideFolder('/Users/me/Dev', tc.path)).toBe(tc.want);
    });
  }
});

describe('withoutTrailingSlash', () => {
  it.each([
    ['/Users/me/Dev/', '/Users/me/Dev'],
    ['/Users/me/Dev', '/Users/me/Dev'],
    ['/', '/'],
  ])('%s becomes %s', (path, want) => {
    expect(withoutTrailingSlash(path)).toBe(want);
  });
});
