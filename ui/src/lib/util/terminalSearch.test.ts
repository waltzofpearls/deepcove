import { describe, expect, it } from 'vitest';
import { isSearchShortcut, searchKeyAction, searchMessage, searchReply } from './terminalSearch';

describe('isSearchShortcut', () => {
  const cases: {
    name: string;
    event: { key: string; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean };
    want: boolean;
  }[] = [
    {
      name: 'Cmd+F',
      event: { key: 'f', metaKey: true, ctrlKey: false, shiftKey: false },
      want: true,
    },
    {
      name: 'Ctrl+Shift+F',
      event: { key: 'F', metaKey: false, ctrlKey: true, shiftKey: true },
      want: true,
    },
    {
      name: 'Ctrl+F stays the shell key',
      event: { key: 'f', metaKey: false, ctrlKey: true, shiftKey: false },
      want: false,
    },
    {
      name: 'a plain f',
      event: { key: 'f', metaKey: false, ctrlKey: false, shiftKey: false },
      want: false,
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(isSearchShortcut(tc.event)).toBe(tc.want);
    });
  }
});

describe('searchKeyAction', () => {
  const cases: {
    name: string;
    event: { key: string; shiftKey: boolean; isComposing: boolean };
    want: string | undefined;
  }[] = [
    {
      name: 'Enter goes older',
      event: { key: 'Enter', shiftKey: false, isComposing: false },
      want: 'older',
    },
    {
      name: 'Shift+Enter goes newer',
      event: { key: 'Enter', shiftKey: true, isComposing: false },
      want: 'newer',
    },
    {
      name: 'Escape closes',
      event: { key: 'Escape', shiftKey: false, isComposing: false },
      want: 'close',
    },
    {
      name: 'a letter is typing',
      event: { key: 'a', shiftKey: false, isComposing: false },
      want: undefined,
    },
    {
      name: 'Enter that picks an input method candidate',
      event: { key: 'Enter', shiftKey: false, isComposing: true },
      want: undefined,
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(searchKeyAction(tc.event)).toBe(tc.want);
    });
  }
});

describe('searchReply', () => {
  const cases: { name: string; data: unknown; want: ReturnType<typeof searchReply> }[] = [
    {
      name: 'a match',
      data: '{"type":"search","searchId":3,"isFound":true}',
      want: { searchId: 3 },
    },
    {
      name: 'no match',
      data: '{"type":"search","searchId":3,"isFound":false}',
      want: { searchId: 3, problem: 'No matches' },
    },
    {
      name: 'a search the server could not run',
      data: '{"type":"search","searchId":3,"isFound":false,"isFailed":true}',
      want: { searchId: 3, problem: 'Search failed' },
    },
    { name: 'another kind of frame', data: '{"type":"notice"}', want: undefined },
    { name: 'not JSON', data: 'hello', want: undefined },
    { name: 'terminal bytes', data: new ArrayBuffer(4), want: undefined },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(searchReply(tc.data)).toEqual(tc.want);
    });
  }
});

describe('searchMessage', () => {
  const cases: { name: string; text: string; isSent: boolean }[] = [
    { name: 'a normal search', text: 'token expired', isSent: true },
    { name: 'a pasted wall of text', text: 'a'.repeat(5000), isSent: false },
    {
      name: 'fewer characters than the limit, but more bytes',
      text: '日'.repeat(1400),
      isSent: false,
    },
    { name: 'quotes that grow when escaped', text: '"'.repeat(2100), isSent: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      const message = searchMessage(tc.text, 'older', 1);
      expect(message !== undefined).toBe(tc.isSent);
      if (message)
        expect(JSON.parse(message)).toEqual({
          type: 'search',
          text: tc.text,
          direction: 'older',
          searchId: 1,
        });
    });
  }
});
