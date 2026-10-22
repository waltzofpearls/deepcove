import { describe, it, expect } from 'vitest';
import {
  cleanPath,
  looksLikePath,
  pathInsideFolder,
  pickerQuery,
  sessionFolderIn,
  splitPath,
} from './path';

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

describe('pickerQuery', () => {
  it.each([
    { input: '~', dir: undefined, prefix: '' },
    { input: '~/', dir: undefined, prefix: '' },
    { input: '~/Dev/Pe', dir: 'Dev', prefix: 'Pe' },
    { input: '~/Dev/', dir: 'Dev/', prefix: '' },
    { input: '/Users/you/Dev/Pe', dir: '/Users/you/Dev', prefix: 'Pe' },
    { input: 'Dev/Pe', dir: 'Dev', prefix: 'Pe' },
  ])('splits "$input"', ({ input, dir, prefix }) => {
    expect(pickerQuery(input)).toEqual({ dir, prefix });
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
    { name: 'a dot is the folder itself', path: '/Users/me/Dev/.', want: '' },
    { name: 'a detour through another folder', path: '/Users/me/code/../Dev/app', want: 'app' },
    { name: 'climbing out', path: '/Users/me/Dev/../x', want: null },
    { name: 'a path that is not a full path', path: 'Users/me/Dev/app', want: null },
    { name: 'a relative climb', path: '../Users/me/Dev/app', want: null },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(pathInsideFolder('/Users/me/Dev', tc.path)).toBe(tc.want);
    });
  }

  it('cleans the folder too', () => {
    expect(pathInsideFolder('/Users/me/Dev/.', '/Users/me/Dev/app')).toBe('app');
    expect(pathInsideFolder('/', '/Users')).toBe('Users');
  });
});

describe('cleanPath', () => {
  it.each([
    ['/Users/me/Dev/', '/Users/me/Dev'],
    ['/Users/me/Dev', '/Users/me/Dev'],
    ['/', '/'],
    ['/Users/me/Dev/.', '/Users/me/Dev'],
    ['/Users/me/code/../Dev', '/Users/me/Dev'],
    ['//Users//me', '/Users/me'],
    ['/..', '/'],
  ])('%s becomes %s', (path, want) => {
    expect(cleanPath(path)).toBe(want);
  });
});

describe('sessionFolderIn', () => {
  it.each([
    ['/Users/me/Dev', 'test123', '/Users/me/Dev/test123'],
    ['/Users/me/Dev/', 'test123', '/Users/me/Dev/test123'],
    ['/Users/me/Dev', ' org/repo ', '/Users/me/Dev/org/repo'],
    ['/', 'test123', '/test123'],
  ])('%s and %s give %s', (root, name, want) => {
    expect(sessionFolderIn(root, name)).toBe(want);
  });
});
