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

// shortId truncates a uuid (or any long id) for compact display.
export function shortId(id: string, len = 12): string {
  return id.length > len ? id.slice(0, len) : id;
}
