// releaseParts reads the major, minor and patch from a version like v1.3.0 or v1.2.0-2-gabc-dirty.
function releaseParts(version: string): number[] | undefined {
  const match = /^v?(\d+)\.(\d+)\.(\d+)/.exec(version);
  return match ? match.slice(1).map(Number) : undefined;
}

// isBehind reports whether a node runs an older release than this UI. A dev build is never behind.
export function isBehind(nodeVersion: string, uiVersion: string): boolean {
  const node = releaseParts(nodeVersion);
  const ui = releaseParts(uiVersion);
  if (!node || !ui) return false;
  for (let part = 0; part < 3; part++) {
    if (node[part] !== ui[part]) return node[part] < ui[part];
  }
  return false;
}
