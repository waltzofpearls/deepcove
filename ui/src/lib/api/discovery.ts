import type { NodeStatus, TailnetStatus } from 'tailscale-web';
import type { NodeInfo, SessionHost } from './types';

// Pure helpers for dashboard-mode discovery: turning the in-browser Tailscale node's view of the
// tailnet (network.status()) into the user's deepcove nodes. Kept out of the .svelte runner so they're
// unit-testable in the node-env vitest. Mirrors the host-side tailnet.filterDeepcoveNodes so the two
// transports (server-side fan-out and browser-side fan-out) discover the same set.

// HOSTNAME_PREFIX marks a deepcove node on the tailnet; WASM_CLIENT_OS is what an in-browser
// tailscale-web node reports (GOOS=js), which carries the prefix too but never hosts the API.
const HOSTNAME_PREFIX = 'deepcove-';
const WASM_CLIENT_OS = 'js';

function stripTrailingDot(s: string): string {
	return s.endsWith('.') ? s.slice(0, -1) : s;
}

// discoverableNodes filters a tailnet status to the user's deepcove hosting nodes: peers owned by the
// same Tailscale user as self, whose hostname carries the deepcove- prefix, excluding in-browser WASM
// clients. Sorted by hostname for stable ordering. Returns [] when status has no self (not up yet).
export function discoverableNodes(status: TailnetStatus | null | undefined): NodeInfo[] {
	const self = status?.self;
	if (!self) return [];
	const nodes = (status?.peers ?? [])
		.filter(
			(p) =>
				p.userId === self.userId &&
				p.hostName.startsWith(HOSTNAME_PREFIX) &&
				p.os !== WASM_CLIENT_OS,
		)
		.map(toNodeInfo);
	nodes.sort((a, b) => a.hostname.localeCompare(b.hostname));
	return nodes;
}

// discoverableWebNodes filters a tailnet status to the user's OTHER in-browser web nodes: same-user,
// deepcove-prefixed, online peers that report the wasm client OS. This browser's own node (status.self)
// is handled separately by the caller, so it is not included here. Online-only, since web nodes are
// ephemeral and churn. Mirrors host-side tailnet.filterWebNodes.
export function discoverableWebNodes(status: TailnetStatus | null | undefined): NodeInfo[] {
	const self = status?.self;
	if (!self) return [];
	const nodes = (status?.peers ?? [])
		.filter(
			(p) =>
				p.userId === self.userId &&
				p.hostName.startsWith(HOSTNAME_PREFIX) &&
				p.os === WASM_CLIENT_OS &&
				p.online,
		)
		.map(toNodeInfo);
	nodes.sort((a, b) => a.hostname.localeCompare(b.hostname));
	return nodes;
}

function toNodeInfo(p: NodeStatus): NodeInfo {
	return {
		stable_node_id: p.id,
		hostname: p.hostName,
		dns_name: stripTrailingDot(p.dnsName),
		os: p.os,
		online: p.online,
		self: false,
	};
}

// pickPrimary chooses the node for incidental app-level calls (agent registry, suggest-name, the global
// preflight banner): the first online node by hostname order, or null if none are online. It is stateless
// so it self-heals as nodes come and go. The sessions list does NOT depend on it (that fans out across
// every node), so a primary going offline never stalls the cross-node view, only re-points these reads.
export function pickPrimary(nodes: NodeInfo[]): NodeInfo | null {
	return nodes.find((n) => n.online) ?? null;
}

// selfHost maps the browser's own tailnet node to a SessionHost so the aggregated list carries a self
// identity. The browser never hosts sessions, so every session reads as remote (badged with its host),
// exactly what the no-node dashboard view wants.
export function selfHost(status: TailnetStatus | null | undefined): SessionHost | null {
	const self = status?.self;
	if (!self) return null;
	return {
		stable_node_id: self.id,
		hostname: self.hostName,
		dns_name: stripTrailingDot(self.dnsName),
	};
}
