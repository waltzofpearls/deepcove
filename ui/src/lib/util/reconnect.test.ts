import { describe, expect, it } from 'vitest';
import { reconnectAction, type ReconnectAction } from './reconnect';

describe('reconnectAction', () => {
  const base = { maxAttempts: 8, earlyCloseThresholdMs: 3000 };
  const cases: {
    name: string;
    everOpened: boolean;
    everConnected: boolean;
    lifetimeMs: number;
    attempts: number;
    want: ReconnectAction;
  }[] = [
    {
      name: 'a working stream that drops waits for the session to come back',
      everOpened: true,
      everConnected: true,
      lifetimeMs: 120_000,
      attempts: 0,
      want: 'reattach',
    },
    {
      name: 'a stream that dies on startup retries quickly',
      everOpened: true,
      everConnected: true,
      lifetimeMs: 200,
      attempts: 0,
      want: 'retry',
    },
    {
      name: 'a socket that never opened retries quickly',
      everOpened: false,
      everConnected: false,
      lifetimeMs: 50,
      attempts: 2,
      want: 'retry',
    },
    {
      name: 'a socket that hangs then fails without opening still retries',
      everOpened: false,
      everConnected: false,
      lifetimeMs: 10_000,
      attempts: 1,
      want: 'retry',
    },
    {
      name: 'giving up needs a terminal that never once connected',
      everOpened: false,
      everConnected: false,
      lifetimeMs: 50,
      attempts: 8,
      want: 'give-up',
    },
    {
      name: 'a terminal that worked before keeps trying after the quick retries',
      everOpened: false,
      everConnected: true,
      lifetimeMs: 50,
      attempts: 8,
      want: 'reattach',
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(reconnectAction({ ...base, ...tc })).toBe(tc.want);
    });
  }
});
