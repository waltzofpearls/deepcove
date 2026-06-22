// Pure helpers for splitting a Tailscale MagicDNS name into its parts. Kept separate from the wasm/UI
// glue so they can be unit tested without a DOM or a live node.

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
