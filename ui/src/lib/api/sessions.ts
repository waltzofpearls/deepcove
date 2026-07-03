import type { NodeInfo, Session, SessionHost, SessionListResponse } from './types';

// Pure helpers for the cross-node session UI. Kept out of the .svelte store so they're unit-testable
// in the node-env vitest runner (no runes/DOM).

// isRemoteSession reports whether a session lives on a node other than `self`, so the UI can badge it
// with its host. Local sessions, single-node setups, and untagged sessions return false.
export function isRemoteSession(session: Session, self: SessionHost | null): boolean {
	return !!session.host && !!self && session.host.stable_node_id !== self.stable_node_id;
}

// hostForSession returns the host to route a per-session API call to: the session's host when it
// lives on a remote node, otherwise undefined so the call uses the boot transport (local session, or
// a single-node setup). Passing a local session's own host would wrongly force a WASM hop to self, so
// callers route through this rather than handing session.host straight to the client.
export function hostForSession(session: Session, self: SessionHost | null): SessionHost | undefined {
	return isRemoteSession(session, self) ? session.host : undefined;
}

// onlineHostNodes returns the nodes a new session can be created on right now: online ones only.
export function onlineHostNodes(nodes: NodeInfo[]): NodeInfo[] {
	return nodes.filter((node) => node.online);
}

// defaultNodeId picks the default create target: this machine if present, else the first node, else
// "" (which the backend treats as "create here").
export function defaultNodeId(nodes: NodeInfo[]): string {
	const self = nodes.find((node) => node.self);
	return (self ?? nodes[0])?.stable_node_id ?? '';
}

// nodeHost projects a node down to the SessionHost used for tagging and host-routed calls.
export function nodeHost(node: NodeInfo): SessionHost {
	return { stable_node_id: node.stable_node_id, hostname: node.hostname, dns_name: node.dns_name };
}

// mergeAggregated combines per-node /api/sessions/local results into one aggregated response, the same
// way the Go session.ListAggregated does for server-side fan-out: each session is tagged with its host
// node, a node whose fetch failed (sessions === null) is recorded as unreachable, and the merged list is
// sorted by most recent activity. Used by the browser-side fan-out in dashboard mode.
export function mergeAggregated(
	perNode: { node: NodeInfo; sessions: Session[] | null }[],
	self: SessionHost | null,
): SessionListResponse {
	const sessions: Session[] = [];
	const unreachable: SessionHost[] = [];
	for (const { node, sessions: list } of perNode) {
		const host = nodeHost(node);
		if (list === null) {
			unreachable.push(host);
			continue;
		}
		for (const s of list) sessions.push({ ...s, host });
	}
	// Most recent first. ISO-8601 timestamps sort correctly as strings.
	sessions.sort((a, b) => (b.last_activity_at ?? '').localeCompare(a.last_activity_at ?? ''));
	return {
		sessions,
		self: self ?? undefined,
		unreachable: unreachable.length ? unreachable : undefined,
	};
}
