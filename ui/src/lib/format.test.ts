import { describe, expect, it, vi } from 'vitest';
import { relativeAge } from './format';

describe('relativeAge', () => {
  const now = new Date('2026-08-29T12:00:00Z');
  const ago = (seconds: number) => new Date(now.getTime() - seconds * 1000).toISOString();

  const cases: [string, number, string][] = [
    ['seconds', 10, 'just now'],
    ['just under a minute', 59, 'just now'],
    ['exactly a minute', 60, '1 minute ago'],
    ['minutes', 5 * 60, '5 minutes ago'],
    ['one hour', 3600, '1 hour ago'],
    ['hours', 5 * 3600, '5 hours ago'],
    ['one day', 86400, '1 day ago'],
    ['two weeks', 14 * 86400, '2 weeks ago'],
    ['one month', 40 * 86400, '1 month ago'],
    ['one year', 400 * 86400, '1 year ago'],
    ['a future timestamp', -60, 'just now'],
  ];

  for (const [name, seconds, want] of cases) {
    it(name, () => {
      vi.setSystemTime(now);
      expect(relativeAge(ago(seconds))).toBe(want);
    });
  }
});
