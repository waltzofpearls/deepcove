import { describe, it, expect } from 'vitest';
import { splitPath } from './path';

describe('splitPath', () => {
	it.each([
		{ input: '', dir: undefined, prefix: '' },
		{ input: '/Users/you/', dir: '/Users/you/', prefix: '' },
		{ input: '/Users/you/Dev', dir: '/Users/you', prefix: 'Dev' },
		{ input: '/x', dir: '/', prefix: 'x' },
		{ input: 'rel', dir: undefined, prefix: 'rel' },
	])('splits "$input"', ({ input, dir, prefix }) => {
		expect(splitPath(input)).toEqual({ dir, prefix });
	});
});
