// Small display formatters shared across components. Lives at $lib
// root (not $lib/utils) so consumers can `import { ageOf, shortId }
// from '$lib/format'` without colliding with shadcn's cn() helper.

// ageOf converts an ISO timestamp to a compact "Ns/Nm/Nh/Nd" suffix
// for the "last active" column. Returns "0s" for future timestamps
// (clock skew) rather than negative numbers.
export function ageOf(iso: string): string {
  const d = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (d < 60) return `${Math.floor(d)}s`;
  if (d < 3600) return `${Math.floor(d / 60)}m`;
  if (d < 86400) return `${Math.floor(d / 3600)}h`;
  return `${Math.floor(d / 86400)}d`;
}

// Steps from smallest to largest, each with how many seconds it holds. A value uses the largest step
// it fills.
const ageSteps: [Intl.RelativeTimeFormatUnit, number][] = [
  ['minute', 60],
  ['hour', 3600],
  ['day', 86400],
  ['week', 604800],
  ['month', 2629800],
  ['year', 31557600],
];

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: 'always' });

// relativeAge says an age the way people do: "just now", "5 minutes ago", "2 weeks ago".
//
// "just now" covers everything below a whole minute, because the units below floor: a 50 second age with
// a lower cutoff came out as "0 minutes ago".
export function relativeAge(iso: string): string {
  const seconds = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (seconds < 60) return 'just now';
  for (let i = 0; i < ageSteps.length; i++) {
    const [unit, size] = ageSteps[i];
    const next = ageSteps[i + 1];
    if (!next || seconds < next[1]) return relative.format(-Math.floor(seconds / size), unit);
  }
  return 'just now';
}

// shortId truncates a uuid (or any long id) for compact display.
export function shortId(id: string, len = 12): string {
  return id.length > len ? id.slice(0, len) : id;
}
