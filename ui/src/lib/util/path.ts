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

// pickerQuery is splitPath for the folder picker's path box, where a leading ~ means home. The server
// lists a relative path from home, so ~/Dev becomes Dev.
export function pickerQuery(typed: string): { dir: string | undefined; prefix: string } {
  if (typed === '~') return splitPath('');
  return splitPath(typed.startsWith('~/') ? typed.slice(2) : typed);
}

// looksLikePath is the cheap client side check on a host folder: absolute, and not something that was
// never a path at all. The server still decides, since only it can tell whether the folder is there.
export function looksLikePath(value: string): boolean {
  const trimmed = value.trim();
  return trimmed.startsWith('/') && !trimmed.includes('\0');
}

// pathInsideFolder is where path sits inside folder: '' for the folder itself, the rest of the path for
// something inside it, and null for anything outside or not a full path.
export function pathInsideFolder(folder: string, path: string): string | null {
  if (!looksLikePath(folder) || !looksLikePath(path)) return null;
  const root = cleanPath(folder);
  const target = cleanPath(path);
  if (target === root) return '';
  const prefix = root === '/' ? '/' : root + '/';
  if (!target.startsWith(prefix)) return null;
  return target.slice(prefix.length);
}

// cleanPath is an absolute path as the server's filepath.Clean saves it, so a typed "/Users/me/Dev/." or
// "/Users/me/code/../Dev/" matches "/Users/me/Dev".
export function cleanPath(path: string): string {
  const parts: string[] = [];
  for (const part of path.trim().split('/')) {
    if (part === '' || part === '.') continue;
    if (part === '..') parts.pop();
    else parts.push(part);
  }
  return '/' + parts.join('/');
}

// sessionFolderIn is the folder a session named name gets inside root.
export function sessionFolderIn(root: string, name: string): string {
  const base = cleanPath(root);
  return (base === '/' ? '' : base) + '/' + name.trim();
}
