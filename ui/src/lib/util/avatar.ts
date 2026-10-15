// initials is up to two letters for someone's avatar: the first letters of their first two names, or the
// first letter of their login when they have no display name.
export function initials(displayName: string, loginName: string): string {
  const names = displayName.trim().split(/\s+/).filter(Boolean);
  const letters =
    names.length > 0
      ? names.slice(0, 2).map((name) => [...name][0])
      : [[...loginName.trim()][0] ?? '?'];
  return letters.join('').toUpperCase();
}

// avatarHue picks a color for someone from their login, so the same person always gets the same one.
export function avatarHue(loginName: string): number {
  let hash = 0;
  for (const char of loginName) hash = (hash * 31 + (char.codePointAt(0) ?? 0)) >>> 0;
  return hash % 360;
}
