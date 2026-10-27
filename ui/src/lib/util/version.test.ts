import { describe, it, expect } from 'vitest';
import { isBehind } from './version';

describe('isBehind', () => {
  it.each([
    { node: 'v1.0.2', ui: 'v1.3.0', want: true },
    { node: 'v1.3.0', ui: 'v1.3.0', want: false },
    { node: 'v1.4.0', ui: 'v1.3.0', want: false },
    { node: 'v1.2.0-2-g895d466-dirty', ui: 'v1.3.0', want: true },
    { node: 'v1.3.0-dirty', ui: 'v1.3.0', want: false },
    { node: 'v1.10.0', ui: 'v1.9.0', want: false },
    { node: 'dev', ui: 'v1.3.0', want: false },
    { node: 'v1.0.2', ui: 'dev', want: false },
  ])('$node against $ui -> $want', ({ node, ui, want }) => {
    expect(isBehind(node, ui)).toBe(want);
  });
});
