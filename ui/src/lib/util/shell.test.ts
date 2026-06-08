import { describe, it, expect } from 'vitest';
import { shellQuote } from './shell';

describe('shellQuote', () => {
	it.each([
		{ input: 'simple', want: 'simple' },
		{ input: '/Users/you/code', want: '/Users/you/code' },
		{ input: 'ghcr.io/x/y:tag', want: 'ghcr.io/x/y:tag' },
		{ input: 'has space', want: "'has space'" },
		{ input: 'a&b', want: "'a&b'" },
		{ input: "it's", want: "'it'\\''s'" },
	])('quotes "$input"', ({ input, want }) => {
		expect(shellQuote(input)).toBe(want);
	});
});
