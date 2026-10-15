import { describe, expect, it } from 'vitest';
import { agentBadgeClass } from './agentBadge';

describe('agentBadgeClass', () => {
  it.each([
    ['claude', 'bg-[#DE7356]/10'],
    ['codex', 'dark:text-white'],
    ['someone-new', 'bg-secondary'],
  ])('%s', (agent, want) => {
    expect(agentBadgeClass(agent)).toContain(want);
  });
});
