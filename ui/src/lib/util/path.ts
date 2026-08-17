// splitPath divides a host path into the directory to list and the leaf prefix to
// filter by, for the host-path autocomplete (see HostPathField). Rules:
//   ""           → { dir: undefined, prefix: '' }   (undefined dir = server default / $HOME)
//   "/a/b/"      → { dir: '/a/b/',   prefix: '' }    (trailing slash: list that dir, no filter)
//   "/a/b/cd"    → { dir: '/a/b',    prefix: 'cd' }  (filter /a/b by 'cd')
//   "/x"         → { dir: '/',       prefix: 'x' }
//   "rel"        → { dir: undefined, prefix: 'rel' } (no slash yet)
export function splitPath(v: string): { dir: string | undefined; prefix: string } {
  if (!v) return { dir: undefined, prefix: '' };
  if (v.endsWith('/')) return { dir: v, prefix: '' };
  const idx = v.lastIndexOf('/');
  if (idx < 0) return { dir: undefined, prefix: v };
  return { dir: v.slice(0, idx) || '/', prefix: v.slice(idx + 1) };
}

// looksLikePath is the cheap client side check on a host folder: absolute, and not something that was
// never a path at all. The server still decides, since only it can tell whether the folder is there.
export function looksLikePath(value: string): boolean {
  const trimmed = value.trim();
  return trimmed.startsWith('/') && !trimmed.includes('\0');
}
