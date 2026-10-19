import { describe, expect, it } from 'vitest';
import {
  mobileKeySequence,
  keyboardShown,
  nextCtrlState,
  withCtrl,
  type CtrlState,
  type MobileKey,
} from './mobileKeyBar';

describe('mobileKeySequence', () => {
  const cases: {
    name: string;
    key: MobileKey;
    applicationCursorKeys?: boolean;
    ctrl?: boolean;
    want: string;
  }[] = [
    { name: 'up in a shell', key: 'up', want: '\x1b[A' },
    { name: 'down in a shell', key: 'down', want: '\x1b[B' },
    { name: 'right in a shell', key: 'right', want: '\x1b[C' },
    { name: 'left in a shell', key: 'left', want: '\x1b[D' },
    { name: 'up in a full screen program', key: 'up', applicationCursorKeys: true, want: '\x1bOA' },
    {
      name: 'left in a full screen program',
      key: 'left',
      applicationCursorKeys: true,
      want: '\x1bOD',
    },
    { name: 'ctrl and left jumps a word', key: 'left', ctrl: true, want: '\x1b[1;5D' },
    {
      name: 'ctrl and up ignores the cursor key mode',
      key: 'up',
      applicationCursorKeys: true,
      ctrl: true,
      want: '\x1b[1;5A',
    },
    { name: 'escape', key: 'escape', want: '\x1b' },
    { name: 'tab', key: 'tab', want: '\t' },
    { name: 'shift tab', key: 'shiftTab', want: '\x1b[Z' },
    { name: 'enter', key: 'enter', want: '\r' },
    { name: 'ctrl leaves enter alone', key: 'enter', ctrl: true, want: '\r' },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      const got = mobileKeySequence(tc.key, {
        applicationCursorKeys: tc.applicationCursorKeys ?? false,
        ctrl: tc.ctrl ?? false,
      });
      expect(got).toBe(tc.want);
    });
  }
});

describe('keyboardShown', () => {
  const cases: { name: string; pageHeight: number; visibleHeight: number; want: boolean }[] = [
    { name: 'nothing covers the page', pageHeight: 1133, visibleHeight: 1133, want: false },
    { name: 'the iPad keyboard is up', pageHeight: 1133, visibleHeight: 780, want: true },
    { name: 'the iPhone keyboard is up', pageHeight: 852, visibleHeight: 520, want: true },
    {
      name: 'the shortcut bar with a hardware keyboard is not a keyboard',
      pageHeight: 1133,
      visibleHeight: 1078,
      want: false,
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(keyboardShown(tc.pageHeight, tc.visibleHeight)).toBe(tc.want);
    });
  }
});

describe('nextCtrlState', () => {
  const cases: { name: string; state: CtrlState; want: CtrlState }[] = [
    { name: 'the first tap holds it for one key', state: 'off', want: 'once' },
    { name: 'the second tap locks it on', state: 'once', want: 'locked' },
    { name: 'the third tap turns it off', state: 'locked', want: 'off' },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(nextCtrlState(tc.state)).toBe(tc.want);
    });
  }
});

describe('withCtrl', () => {
  const cases: { name: string; data: string; want: string }[] = [
    { name: 'c interrupts', data: 'c', want: '\x03' },
    { name: 'a capital letter works the same', data: 'C', want: '\x03' },
    { name: 'a goes to the start of the line', data: 'a', want: '\x01' },
    { name: 'open bracket is escape', data: '[', want: '\x1b' },
    { name: 'space is NUL', data: ' ', want: '\x00' },
    { name: 'question mark is delete', data: '?', want: '\x7f' },
    { name: 'a digit has no control code', data: '1', want: '1' },
    { name: 'a predicted word passes unchanged', data: 'hello', want: 'hello' },
    { name: 'sharp s is not Ctrl+S', data: 'ß', want: 'ß' },
    { name: 'dotless i is not Tab', data: 'ı', want: 'ı' },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(withCtrl(tc.data)).toBe(tc.want);
    });
  }
});
