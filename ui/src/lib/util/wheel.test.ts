import { describe, expect, it } from 'vitest';
import { wheelSteps } from './wheel';

describe('wheelSteps', () => {
  const cases: {
    name: string;
    deltaY: number;
    deltaMode: number;
    carry: number;
    wantSteps: number;
  }[] = [
    {
      name: 'a mouse notch in pixels scrolls one step',
      deltaY: -40,
      deltaMode: 0,
      carry: 0,
      wantSteps: -1,
    },
    {
      name: 'scrolling down is positive, matching deltaY',
      deltaY: 40,
      deltaMode: 0,
      carry: 0,
      wantSteps: 1,
    },
    {
      name: 'a big notch scrolls proportionally rather than one line at a time',
      deltaY: -120,
      deltaMode: 0,
      carry: 0,
      wantSteps: -3,
    },
    {
      name: 'a small trackpad delta alone is not yet a step',
      deltaY: -8,
      deltaMode: 0,
      carry: 0,
      wantSteps: 0,
    },
    {
      name: 'leftover pixels carry in, so a slow drag eventually scrolls',
      deltaY: -8,
      deltaMode: 0,
      carry: -36,
      wantSteps: -1,
    },
    {
      name: 'line units are converted, not treated as pixels',
      deltaY: -3,
      deltaMode: 1,
      carry: 0,
      wantSteps: -1,
    },
    {
      name: 'page units scroll a lot',
      deltaY: 1,
      deltaMode: 2,
      carry: 0,
      wantSteps: 10,
    },
    { name: 'no movement is no steps', deltaY: 0, deltaMode: 0, carry: 0, wantSteps: 0 },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(wheelSteps(tc.deltaY, tc.deltaMode, tc.carry).steps).toBe(tc.wantSteps);
    });
  }

  // The carry must not let a gesture invent or lose scrolling over time.
  it('accumulates a stream of small deltas into the same total as one big one', () => {
    let carry = 0;
    let total = 0;
    for (let i = 0; i < 10; i++) {
      const r = wheelSteps(-12, 0, carry);
      carry = r.carry;
      total += r.steps;
    }
    // 10 events x 12px = 120px = 3 steps of 40px.
    expect(total).toBe(-3);
  });

  it('resets cleanly when direction reverses', () => {
    const up = wheelSteps(-30, 0, 0);
    expect(up.steps).toBe(0);
    // Reversing should not carry the upward remainder into a downward step.
    const down = wheelSteps(30, 0, up.carry);
    expect(down.steps).toBe(0);
    expect(down.carry).toBe(0);
  });
});
