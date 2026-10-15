import { describe, expect, it } from 'vitest';
import { avatarHue, initials } from './avatar';

describe('initials', () => {
  it.each([
    ['two names', 'Ana Lima', 'ana@example.com', 'AL'],
    ['three names', 'Ana Maria Lima', 'ana@example.com', 'AM'],
    ['one name', 'ana', 'ana@example.com', 'A'],
    ['no display name', '', 'rollie@example.com', 'R'],
    ['extra spaces', '  ana   lima ', 'ana@example.com', 'AL'],
    ['nothing at all', '', '', '?'],
  ])('%s', (_name, displayName, loginName, want) => {
    expect(initials(displayName, loginName)).toBe(want);
  });
});

describe('avatarHue', () => {
  it('gives the same person the same color every time', () => {
    expect(avatarHue('ana@example.com')).toBe(avatarHue('ana@example.com'));
  });

  it('stays on the color wheel', () => {
    for (const login of ['a', 'ana@example.com', 'someone.with.a.long.name@example.org']) {
      const hue = avatarHue(login);
      expect(hue).toBeGreaterThanOrEqual(0);
      expect(hue).toBeLessThan(360);
    }
  });
});
