import { describe, expect, it } from 'vitest';
import {
  changeCount,
  hasIncompleteNewRow,
  headerEdits,
  rowsFromSaved,
  rowsNeedingValueAgain,
  sameHeaderName,
  type HeaderRow,
} from './llmProxyForm';

const border0 = 'https://rollie-llm-sandbox-rollie-vpn.border0.io';
const saved = {
  url: border0,
  headers: ['Border0-Token', 'X-Team'],
};
const savedRow = (name: string, value = ''): HeaderRow => ({
  id: 0,
  name,
  value,
  saved: true,
});
const newRow = (name: string, value: string): HeaderRow => ({
  id: 9,
  name,
  value,
  saved: false,
});

describe('rowsFromSaved', () => {
  it('lists the saved names with empty values', () => {
    expect(rowsFromSaved(saved).map((row) => [row.name, row.value, row.saved])).toEqual([
      ['Border0-Token', '', true],
      ['X-Team', '', true],
    ]);
  });
});

describe('headerEdits', () => {
  const cases = [
    {
      name: 'a saved header left alone keeps its value',
      rows: [savedRow('Border0-Token')],
      want: [{ name: 'Border0-Token' }],
    },
    {
      name: 'a saved header with a new value sends it',
      rows: [savedRow('Border0-Token', 'b0-new')],
      want: [{ name: 'Border0-Token', value: 'b0-new' }],
    },
    {
      name: 'a new header sends its value',
      rows: [newRow('X-Team', 'infra')],
      want: [{ name: 'X-Team', value: 'infra' }],
    },
    {
      name: 'a removed header is left out',
      rows: [],
      want: [],
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(headerEdits(tc.rows)).toEqual(tc.want);
    });
  }
});

describe('changeCount', () => {
  const cases = [
    {
      name: 'nothing changed',
      url: border0,
      rows: [savedRow('Border0-Token'), savedRow('X-Team')],
      want: 0,
    },
    {
      name: 'surrounding space is not a change',
      url: ` ${border0} `,
      rows: [savedRow('Border0-Token'), savedRow('X-Team')],
      want: 0,
    },
    {
      name: 'a new URL',
      url: 'https://gateway.example.com',
      rows: [savedRow('Border0-Token'), savedRow('X-Team')],
      want: 1,
    },
    {
      name: 'a new value, a removed header and a new header',
      url: border0,
      rows: [savedRow('Border0-Token', 'b0-new'), newRow('X-Region', 'us')],
      want: 3,
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(changeCount(saved, tc.url, tc.rows)).toBe(tc.want);
    });
  }
});

describe('hasIncompleteNewRow', () => {
  const cases = [
    { name: 'a new header with both', rows: [newRow('X-Team', 'infra')], want: false },
    { name: 'a new header with no name', rows: [newRow(' ', 'infra')], want: true },
    { name: 'a new header with no value', rows: [newRow('X-Team', '')], want: true },
    { name: 'a saved header with no value keeps it', rows: [savedRow('X-Team')], want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(hasIncompleteNewRow(tc.rows)).toBe(tc.want);
    });
  }
});

describe('rowsNeedingValueAgain', () => {
  const rows = [savedRow('Border0-Token', 'gw-1234'), savedRow('X-Team')];
  const cases = [
    { name: 'the same URL', url: border0, want: [] },
    {
      name: 'a new port or scheme on the same host',
      url: 'http://rollie-llm-sandbox-rollie-vpn.border0.io:8443',
      want: [],
    },
    {
      name: 'the host name in another case',
      url: 'https://Rollie-LLM-Sandbox-Rollie-VPN.border0.io',
      want: [],
    },
    { name: 'a new host', url: 'https://gateway.example.com', want: ['X-Team'] },
    { name: 'a cleared URL', url: '', want: ['X-Team'] },
    { name: 'a URL that does not parse yet', url: 'gateway', want: [] },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(rowsNeedingValueAgain(saved, tc.url, rows).map((row) => row.name)).toEqual(tc.want);
    });
  }

  it('nothing saved yet', () => {
    const nothingSaved = { url: '', headers: [] };
    expect(rowsNeedingValueAgain(nothingSaved, border0, [newRow('X-Team', 'infra')])).toEqual([]);
  });
});

describe('sameHeaderName', () => {
  const cases = [
    {
      name: 'typed in another case',
      typed: ' border0-token ',
      reported: 'Border0-Token',
      want: true,
    },
    { name: 'another header', typed: 'X-Team', reported: 'Border0-Token', want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(sameHeaderName(tc.typed, tc.reported)).toBe(tc.want);
    });
  }
});
