// Dashboard-mode cluster discovery. In `deepcove dashboard` (cross-tailnet thin client) there is no
// server node to fan out for us: the in-browser Tailscale node has to find the cluster itself. This
// store brings that node up (reusing ensureTailnet for the login lifecycle), then polls its view of the
// tailnet (network.status()) for the user's deepcove nodes.
//
// What it produces:
//   - nodes: the discovered hosting nodes, the source for the Nodes page, the New Session target list,
//     and the browser-side sessions fan-out (api.listSessionsAcrossNodes).
//   - self:  the browser's own tailnet identity, so the aggregated list has a "self" and every session
//     reads as remote (badged with its host).
//   - a "primary" node, set as the boot transport target for incidental app-level calls (agent
//     registry, suggest-name, preflight). The sessions list never depends on it (it fans out across
//     every node), so a primary going offline only re-points those reads, never stalls the cluster view.
//
// Only used in discover mode (transport.discoveryRequired()); connected mode talks to a server that
// fans out and never touches this.

import { ensureTailnet } from '$lib/api/wasm';
import { discoverableNodes, discoverableWebNodes, pickReachablePrimary, selfHost } from '$lib/api/discovery';
import { setDiscoveredTarget } from '$lib/api/transport';
import type { NodeInfo, SessionHost } from '$lib/api/types';

export type DiscoveryPhase = 'idle' | 'discovering' | 'ready' | 'none' | 'error';

let phase = $state<DiscoveryPhase>('idle');
let nodes = $state<NodeInfo[]>([]);
let webNodes = $state<NodeInfo[]>([]);
let self = $state<SessionHost | null>(null);
let errorMessage = $state<string | null>(null);

let started = false;
let timer: ReturnType<typeof setInterval> | undefined;
let net: Awaited<ReturnType<typeof ensureTailnet>> | undefined;

// Poll the netmap a few times a second-ish; status() is a cheap synchronous read of the in-memory
// netmap. graceEmptyPolls keeps us in "discovering" briefly after login while peers populate, so we
// don't flash "no nodes found" before the netmap has arrived.
const refreshMs = 3000;
const graceEmptyPolls = 3;
let emptyPolls = 0;

const pingTimeoutMs = 3000;
let currentPrimaryDns: string | undefined;
let repicking = false;

// pingAlive probes a peer over the tailnet, racing a timeout since a dead peer's ping can hang.
async function pingAlive(net: Awaited<ReturnType<typeof ensureTailnet>>, dnsName: string): Promise<boolean> {
	try {
		const res = await Promise.race([
			net.ping(dnsName),
			new Promise<never>((_, reject) => setTimeout(() => reject(new Error('ping timeout')), pingTimeoutMs)),
		]);
		return res.alive;
	} catch {
		return false;
	}
}

// refresh updates the node list synchronously (a cheap netmap read, keeps the UI live) and kicks off the
// async primary re-pick.
function refresh(net: Awaited<ReturnType<typeof ensureTailnet>>): void {
	const status = net.status();
	nodes = discoverableNodes(status);
	webNodes = discoverableWebNodes(status);
	self = selfHost(status);
	void repick(net, nodes);
}

// repick points the boot transport at a REACHABLE primary. It keeps the current one if it still pings
// alive (steady state: one ping), else probes all online candidates. Guarded so overlapping polls don't
// pile up while a dead peer's ping times out.
async function repick(net: Awaited<ReturnType<typeof ensureTailnet>>, candidates: NodeInfo[]): Promise<void> {
	if (repicking) return;
	repicking = true;
	try {
		const current = candidates.find((n) => n.dns_name === currentPrimaryDns && n.online);
		let primary = current && (await pingAlive(net, current.dns_name)) ? current : null;
		if (!primary) primary = await pickReachablePrimary(candidates, (dns) => pingAlive(net, dns));
		currentPrimaryDns = primary?.dns_name;
		if (primary) {
			emptyPolls = 0;
			setDiscoveredTarget(primary.dns_name);
			phase = 'ready';
			return;
		}
		// No reachable node this round. Hold "discovering" through the grace window; after it, fall back to
		// the first online node if there is one (so we're never stuck if ping is unavailable/flaky, a
		// maybe unreachable target beats none), else surface "none". The poll keeps running and self-heals.
		emptyPolls++;
		if (emptyPolls >= graceEmptyPolls) {
			const online = candidates.find((n) => n.online && n.dns_name);
			if (online) {
				emptyPolls = 0;
				currentPrimaryDns = online.dns_name;
				setDiscoveredTarget(online.dns_name);
				phase = 'ready';
				return;
			}
			phase = 'none';
		} else if (phase === 'ready') {
			phase = 'none';
		}
	} finally {
		repicking = false;
	}
}

export const discovery = {
	get phase(): DiscoveryPhase {
		return phase;
	},
	get nodes(): NodeInfo[] {
		return nodes;
	},
	get webNodes(): NodeInfo[] {
		return webNodes;
	},
	get self(): SessionHost | null {
		return self;
	},
	get error(): string | null {
		return errorMessage;
	},

	// refreshNow re-reads the netmap immediately (for a manual refresh), between the timer ticks.
	refreshNow(): void {
		const n = net;
		if (!n) return;
		try {
			refresh(n);
		} catch {
			/* status() can throw transiently around reconnects; the next tick retries */
		}
	},

	// start brings up the in-browser node and begins polling for the cluster. Idempotent. The login
	// lifecycle drives tailnetAuth (shown by TailnetGate); discovery proper begins once it's running.
	async start(): Promise<void> {
		if (started) return;
		started = true;
		phase = 'discovering';
		try {
			const n = (net = await ensureTailnet());
			refresh(n);
			timer = setInterval(() => {
				try {
					refresh(n);
				} catch {
					// status() can throw transiently around reconnects; the next tick retries.
				}
			}, refreshMs);
		} catch (err) {
			phase = 'error';
			errorMessage = err instanceof Error ? err.message : String(err);
		}
	},

	// stop halts polling. Exposed mainly for tests/teardown; the dashboard keeps discovery running for
	// the lifetime of the page.
	stop(): void {
		if (timer) clearInterval(timer);
		timer = undefined;
	},
};
