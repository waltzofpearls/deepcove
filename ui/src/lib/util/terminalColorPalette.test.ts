import { describe, it, expect } from 'vitest';
import { withTerminalColorPalette } from './terminalColorPalette';

describe('withTerminalColorPalette', () => {
  const cases: { name: string; html: string; want: string }[] = [
    { name: 'red', html: 'color: #cc0000;', want: 'color: #cc6666;' },
    {
      name: 'bright green background',
      html: 'background-color: #8ae234;',
      want: 'background-color: #b9ca4a;',
    },
    { name: 'upper case', html: 'color: #CC0000;', want: 'color: #cc6666;' },
    { name: 'a color outside the 16 stays', html: 'color: #5f87af;', want: 'color: #5f87af;' },
    {
      name: 'several colors in one line',
      html: '<span style="color: #cc0000;">-</span><span style="color: #4e9a06;">+</span>',
      want: '<span style="color: #cc6666;">-</span><span style="color: #b5bd68;">+</span>',
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(withTerminalColorPalette(tc.html)).toBe(tc.want);
    });
  }
});
