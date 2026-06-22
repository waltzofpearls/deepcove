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
import { discoverableNodes, discoverableWebNodes, pickPrimary, selfHost } from '$lib/api/discovery';
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

// Poll the netmap a few times a second-ish; status() is a cheap synchronous read of the in-memory
// netmap. graceEmptyPolls keeps us in "discovering" briefly after login while peers populate, so we
// don't flash "no nodes found" before the netmap has arrived.
const refreshMs = 3000;
const graceEmptyPolls = 3;
let emptyPolls = 0;

function refresh(net: Awaited<ReturnType<typeof ensureTailnet>>): void {
	const status = net.status();
	nodes = discoverableNodes(status);
	webNodes = discoverableWebNodes(status);
	self = selfHost(status);

	const primary = pickPrimary(nodes);
	if (primary) {
		emptyPolls = 0;
		setDiscoveredTarget(primary.dns_name);
		phase = 'ready';
		return;
	}
	// No online node. Hold "discovering" through the grace window after first coming up; once we've
	// been ready (or waited long enough), surface "none" so the gate can explain. The poll keeps
	// running, so the view self-heals the moment a node appears.
	emptyPolls++;
	if (phase === 'ready' || emptyPolls >= graceEmptyPolls) phase = 'none';
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

	// start brings up the in-browser node and begins polling for the cluster. Idempotent. The login
	// lifecycle drives tailnetAuth (shown by TailnetGate); discovery proper begins once it's running.
	async start(): Promise<void> {
		if (started) return;
		started = true;
		phase = 'discovering';
		try {
			const net = await ensureTailnet();
			refresh(net);
			timer = setInterval(() => {
				try {
					refresh(net);
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
