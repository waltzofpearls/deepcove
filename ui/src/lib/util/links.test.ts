import { describe, it, expect } from 'vitest';
import { opensInNewTab } from './links';

describe('opensInNewTab', () => {
  // Tests run without a DOM, so a click is the fields opensInNewTab reads.
  const plain = { metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, button: 0 };
  const cases: { name: string; init: Partial<typeof plain>; want: boolean }[] = [
    { name: 'a plain click stays here', init: {}, want: false },
    { name: 'cmd click', init: { metaKey: true }, want: true },
    { name: 'ctrl click', init: { ctrlKey: true }, want: true },
    { name: 'shift click', init: { shiftKey: true }, want: true },
    { name: 'middle click', init: { button: 1 }, want: true },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(opensInNewTab({ ...plain, ...tc.init } as MouseEvent)).toBe(tc.want);
    });
  }
});
