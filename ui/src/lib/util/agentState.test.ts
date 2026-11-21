import { describe, expect, it } from 'vitest';
import type { Session } from '$lib/api/types';
import { isChangingSoon, statusLabel } from './agentState';

describe('isChangingSoon', () => {
  const cases: { name: string; session: Partial<Session>; want: boolean }[] = [
    { name: 'starting', session: { status: 'starting' }, want: true },
    { name: 'working', session: { status: 'running', agent_state: 'working' }, want: true },
    { name: 'needs you', session: { status: 'running', agent_state: 'needs_you' }, want: true },
    { name: 'done', session: { status: 'running', agent_state: 'done' }, want: false },
    { name: 'stopped', session: { status: 'stopped' }, want: false },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(isChangingSoon(tc.session as Session)).toBe(tc.want);
    });
  }
});

describe('statusLabel', () => {
  const cases: { name: string; session: Partial<Session>; want: string }[] = [
    { name: 'working', session: { status: 'running', agent_state: 'working' }, want: 'Working' },
    {
      name: 'needs you, with what for',
      session: {
        status: 'running',
        agent_state: 'needs_you',
        agent_state_detail: 'permission prompt',
      },
      want: 'Needs you: permission prompt',
    },
    {
      name: 'error, with what failed',
      session: { status: 'running', agent_state: 'error', agent_state_detail: 'API Error: 529' },
      want: 'Error: API Error: 529',
    },
    { name: 'running, nothing known yet', session: { status: 'running' }, want: 'Running' },
    { name: 'stopped', session: { status: 'stopped', agent_state: 'idle' }, want: 'Stopped' },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      expect(statusLabel(tc.session as Session)).toBe(tc.want);
    });
  }
});
