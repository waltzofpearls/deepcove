import type { Agent, LoginMode } from '$lib/api/types';

// LoginChoice is one row of the login picker.
export interface LoginChoice {
  mode: LoginMode;
  label: string;
  detail: string;
  // Set when the row shows but cannot be picked yet, saying what is missing.
  unavailable?: string;
  // Where the requests go, shown on the choice itself, like the LLM proxy's URL.
  address?: string;
}

// Where the session runs decides its choices: one login for a whole workspace VM, or the machine's own
// login with no sandbox.
export type LoginPlace = 'session_vm' | 'workspace_vm' | 'no_sandbox';

// loginChoices lists the ways a session can log in, safest first, from what the agent allows and what
// this node has set up.
export function loginChoices(
  agent: Pick<Agent, 'login_modes' | 'login_hint'>,
  place: LoginPlace,
  setup: { proxyUrl: string; hasApiKey: boolean; apiKeyEnding: string },
): LoginChoice[] {
  const choices: LoginChoice[] = [];
  const allows = (mode: LoginMode) => agent.login_modes.includes(mode);
  if (place === 'workspace_vm') {
    if (allows('shared')) {
      choices.push({
        mode: 'shared',
        label: "The workspace VM's login",
        detail: 'Every session on this VM uses one login, set by your default in Settings.',
      });
    }
  } else {
    if (allows('shared') && place === 'session_vm') {
      choices.push({
        mode: 'shared',
        label: 'My shared login',
        detail: 'Log in once, and every session that shares uses it.',
      });
    }
    if (allows('in_session')) {
      choices.push(
        place === 'no_sandbox'
          ? {
              mode: 'in_session',
              label: 'The login on this machine',
              detail: 'Whatever this machine is already logged in as.',
            }
          : { mode: 'in_session', label: 'Its own login', detail: agent.login_hint },
      );
    }
  }
  if (allows('api_key') && place !== 'no_sandbox') {
    choices.push({
      mode: 'api_key',
      label: 'My API key',
      detail: setup.apiKeyEnding
        ? `Billed to your API account. Key ending ${setup.apiKeyEnding}.`
        : 'Billed to your API account.',
      unavailable: setup.hasApiKey ? undefined : 'Add a key in Settings first.',
    });
  }
  if (allows('llm_proxy') && setup.proxyUrl) {
    choices.push({
      mode: 'llm_proxy',
      label: 'LLM proxy',
      detail: `${setup.proxyUrl} supplies the key.`,
      address: setup.proxyUrl,
    });
  }
  return choices;
}

// changesAgentEnv mirrors the Go rule: moving to or from a mode the host routes restarts the agent.
export function changesAgentEnv(from: LoginMode, to: LoginMode): boolean {
  const routed = (mode: LoginMode) => mode === 'api_key' || mode === 'llm_proxy';
  return from !== to && (routed(from) || routed(to));
}

// loginFallsBack says whether a mode is unusable among choices, so the session gets its own login instead.
export function loginFallsBack(mode: LoginMode, choices: LoginChoice[]): boolean {
  const choice = choices.find((candidate) => candidate.mode === mode);
  return mode !== 'in_session' && (!choice || !!choice.unavailable);
}
