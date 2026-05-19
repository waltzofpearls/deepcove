import { type ClassValue, clsx } from 'clsx';
import { twMerge } from 'tailwind-merge';

// cn merges Tailwind utility classes with conflict resolution.
// The standard shadcn-svelte helper — used by every vendored
// component so class overrides from props work cleanly.
export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}
