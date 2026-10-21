// Pure helpers for Tailscale node names. Kept separate from the wasm/UI glue so they can be unit
// tested without a DOM or a live node.

import Bowser from 'bowser';

// machineName returns a node's machine name: the first label of its MagicDNS name. That label carries
// Tailscale's dedup suffix (e.g. deepcove-web-8.tail9c1f4.ts.net -> deepcove-web-8), which the requested
// hostname does not, so this is what matches the Tailscale admin. Empty in, empty out.
export function machineName(dnsName: string): string {
  if (!dnsName) return '';
  return dnsName.replace(/\.$/, '').split('.')[0];
}

// tailnetDomain returns the tailnet domain: the MagicDNS name with its first (host) label stripped
// (e.g. deepcove-web-8.tail9c1f4.ts.net -> tail9c1f4.ts.net). Empty when there is no domain part.
export function tailnetDomain(dnsName: string): string {
  const name = dnsName.replace(/\.$/, '');
  const dot = name.indexOf('.');
  return dot === -1 ? '' : name.slice(dot + 1);
}

// webNodeHostname names a browser's node after its device and browser, like deepcove-ipad-firefox,
// so the Tailscale admin can tell them apart. Falls back to deepcove-web when neither is known.
export function webNodeHostname(userAgent: string, touchPoints: number): string {
  const parsed = Bowser.parse(userAgent);
  const parts = [
    deviceName(parsed.os.name ?? '', parsed.platform.type ?? '', touchPoints),
    browserName(parsed.browser.name ?? ''),
  ].filter(Boolean);
  return parts.length ? ['deepcove', ...parts].join('-') : 'deepcove-web';
}

// iPad Safari says it is a Mac, so touch points decide.
function deviceName(os: string, platform: string, touchPoints: number): string {
  if (os === 'iOS') return platform === 'tablet' ? 'ipad' : 'iphone';
  if (os === 'macOS') return touchPoints > 1 ? 'ipad' : 'mac';
  if (os === 'Android') return platform === 'tablet' ? 'android-tablet' : 'android';
  if (os === 'Chrome OS') return 'chromebook';
  return hostnameLabel(os);
}

// Bowser's own short key, so "Microsoft Edge" becomes edge.
function browserName(name: string): string {
  const key = Object.entries(Bowser.BROWSER_MAP).find(([, fullName]) => fullName === name)?.[0];
  return hostnameLabel(key ?? name);
}

function hostnameLabel(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '');
}
