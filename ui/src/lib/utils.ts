import { type ClassValue, clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

// cn merges Tailwind utility classes with conflict resolution.
// The standard shadcn-svelte helper — used by every vendored
// component so class overrides from props work cleanly.
export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}

// Standard shadcn-svelte prop-type helpers. The vendored ui/ components import
// these from here (e.g. `WithElementRef<HTMLButtonAttributes>`); without them
// svelte-check fails across every shadcn component.
export type WithElementRef<T, U extends HTMLElement = HTMLElement> = T & {
	ref?: U | null;
};

export type WithoutChild<T> = T extends { child?: unknown } ? Omit<T, 'child'> : T;

export type WithoutChildren<T> = T extends { children?: unknown } ? Omit<T, 'children'> : T;

export type WithoutChildrenOrChild<T> = WithoutChildren<WithoutChild<T>>;
