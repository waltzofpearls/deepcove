import type { Session } from '$lib/api/types';

// How often sessions are polled while one may change any moment, so a finished turn shows within 2s.
export const busyPollIntervalMs = 2_000;

// isChangingSoon is a session that is starting, or whose agent is on a turn or waiting on you.
export function isChangingSoon(session: Session): boolean {
  return (
    session.status === 'starting' ||
    session.agent_state === 'working' ||
    session.agent_state === 'needs_you'
  );
}

const agentStateLabels = {
  working: 'Working',
  needs_you: 'Needs you',
  done: 'Done',
  idle: 'Idle',
  error: 'Error',
};

// statusLabel says what a session's dot means, with what the agent waits for or what failed.
export function statusLabel(
  session: Pick<Session, 'status' | 'agent_state' | 'agent_state_detail'>,
): string {
  if (session.status !== 'running') {
    return session.status.charAt(0).toUpperCase() + session.status.slice(1);
  }
  if (!session.agent_state) return 'Running';
  const label = agentStateLabels[session.agent_state];
  return session.agent_state_detail ? `${label}: ${session.agent_state_detail}` : label;
}
