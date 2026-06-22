import { describe, it, expect } from 'vitest';
import { machineName, tailnetDomain } from './tailnet';

describe('machineName', () => {
	it.each([
		{ dnsName: 'deepcove-web-8.tail9c1f4.ts.net', want: 'deepcove-web-8' },
		{ dnsName: 'deepcove-topbasslabs-5.tail9c1f4.ts.net.', want: 'deepcove-topbasslabs-5' },
		{ dnsName: 'deepcove-web.tailnet.ts.net', want: 'deepcove-web' },
		{ dnsName: 'bare-host', want: 'bare-host' },
		{ dnsName: '', want: '' },
	])('$dnsName -> $want', ({ dnsName, want }) => {
		expect(machineName(dnsName)).toBe(want);
	});
});

describe('tailnetDomain', () => {
	it.each([
		{ dnsName: 'deepcove-web-8.tail9c1f4.ts.net', want: 'tail9c1f4.ts.net' },
		{ dnsName: 'deepcove-topbasslabs-5.tail9c1f4.ts.net.', want: 'tail9c1f4.ts.net' },
		{ dnsName: 'host.example.com', want: 'example.com' },
		{ dnsName: 'bare-host', want: '' },
		{ dnsName: '', want: '' },
	])('$dnsName -> $want', ({ dnsName, want }) => {
		expect(tailnetDomain(dnsName)).toBe(want);
	});
});
