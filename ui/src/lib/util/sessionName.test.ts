import { describe, expect, it } from 'vitest';
import { shortSessionName } from './sessionName';

describe('shortSessionName', () => {
  it.each([
    ['helpme', 'helpme'],
    ['abcdefghijklmnopqrst', 'abcdefghijklmnopqrst'],
    ['abcdefghijklmnopqrstu', 'abcdefghij…mnopqrstu'],
    ['border0-cli-rdp-proxy-debug-build', 'border0-cl…bug-build'],
    ['ñññññññññññññññññññññ', 'ññññññññññ…ñññññññññ'],
  ])('%s', (name, want) => {
    expect(shortSessionName(name)).toBe(want);
  });
});
