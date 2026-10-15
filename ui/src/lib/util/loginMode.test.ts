import { describe, expect, it } from 'vitest';
import { changesAgentEnv, loginChoices, loginFallsBack, type LoginPlace } from './loginMode';
import type { LoginMode } from '$lib/api/types';

const claude = {
  login_modes: ['in_session', 'shared', 'api_key', 'llm_proxy'] as LoginMode[],
  login_hint: 'Run /login in this session.',
};

describe('loginChoices', () => {
  const cases: {
    name: string;
    place: LoginPlace;
    proxyUrl?: string;
    hasApiKey?: boolean;
    apiKeyEnding?: string;
    want: string[];
  }[] = [
    {
      name: 'a session VM with nothing set up',
      place: 'session_vm',
      want: ['shared', 'in_session', 'api_key!'],
    },
    {
      name: 'a session VM with a key and a proxy',
      place: 'session_vm',
      proxyUrl: 'http://proxy:9000',
      hasApiKey: true,
      apiKeyEnding: 'wxyz',
      want: ['shared', 'in_session', 'api_key', 'llm_proxy'],
    },
    {
      name: 'the workspace VM',
      place: 'workspace_vm',
      hasApiKey: true,
      apiKeyEnding: 'wxyz',
      want: ['shared', 'api_key'],
    },
    {
      name: 'a key too short to show an ending',
      place: 'session_vm',
      hasApiKey: true,
      want: ['shared', 'in_session', 'api_key'],
    },
    {
      name: 'no sandbox',
      place: 'no_sandbox',
      proxyUrl: 'http://proxy:9000',
      want: ['in_session', 'llm_proxy'],
    },
  ];
  for (const tc of cases) {
    it(tc.name, () => {
      const got = loginChoices(claude, tc.place, {
        proxyUrl: tc.proxyUrl ?? '',
        hasApiKey: tc.hasApiKey ?? false,
        apiKeyEnding: tc.apiKeyEnding ?? '',
      }).map((choice) => choice.mode + (choice.unavailable ? '!' : ''));
      expect(got).toEqual(tc.want);
    });
  }

  it('offers only what the agent allows', () => {
    const got = loginChoices({ login_modes: ['in_session'], login_hint: '' }, 'session_vm', {
      proxyUrl: 'http://proxy:9000',
      hasApiKey: true,
      apiKeyEnding: 'wxyz',
    });
    expect(got.map((choice) => choice.mode)).toEqual(['in_session']);
  });

  it('tells you how to log in to this agent inside the session', () => {
    const codex = {
      login_modes: ['in_session'] as LoginMode[],
      login_hint: 'Sign in with a device code.',
    };
    const got = loginChoices(codex, 'session_vm', {
      proxyUrl: '',
      hasApiKey: false,
      apiKeyEnding: '',
    });
    expect(got[0].detail).toBe('Sign in with a device code.');
  });
});

describe('changesAgentEnv', () => {
  const cases: [LoginMode, LoginMode, boolean][] = [
    ['shared', 'in_session', false],
    ['shared', 'api_key', true],
    ['llm_proxy', 'shared', true],
    ['api_key', 'api_key', false],
  ];
  for (const [from, to, want] of cases) {
    it(`${from} to ${to}`, () => expect(changesAgentEnv(from, to)).toBe(want));
  }
});

describe('loginFallsBack', () => {
  const choices = loginChoices(claude, 'session_vm', {
    proxyUrl: '',
    hasApiKey: false,
    apiKeyEnding: '',
  });
  const cases: [LoginMode, boolean][] = [
    ['shared', false],
    ['in_session', false],
    ['api_key', true],
    ['llm_proxy', true],
  ];
  for (const [mode, want] of cases) {
    it(`${mode} with no key and no proxy`, () => expect(loginFallsBack(mode, choices)).toBe(want));
  }
});
