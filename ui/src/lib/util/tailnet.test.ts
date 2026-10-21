import { describe, it, expect } from 'vitest';
import { machineName, tailnetDomain, webNodeHostname } from './tailnet';

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

describe('webNodeHostname', () => {
  const cases: { name: string; userAgent: string; touchPoints: number; want: string }[] = [
    {
      name: 'iphone safari',
      userAgent:
        'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1',
      touchPoints: 5,
      want: 'deepcove-iphone-safari',
    },
    {
      name: 'iphone chrome',
      userAgent:
        'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0.6723.90 Mobile/15E148 Safari/604.1',
      touchPoints: 5,
      want: 'deepcove-iphone-chrome',
    },
    {
      name: 'ipad safari says it is a mac',
      userAgent:
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15',
      touchPoints: 5,
      want: 'deepcove-ipad-safari',
    },
    {
      name: 'ipad firefox',
      userAgent:
        'Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/132.0 Mobile/15E148 Safari/605.1.15',
      touchPoints: 5,
      want: 'deepcove-ipad-firefox',
    },
    {
      name: 'mac safari',
      userAgent:
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15',
      touchPoints: 0,
      want: 'deepcove-mac-safari',
    },
    {
      name: 'mac firefox',
      userAgent:
        'Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:132.0) Gecko/20100101 Firefox/132.0',
      touchPoints: 0,
      want: 'deepcove-mac-firefox',
    },
    {
      name: 'windows edge',
      userAgent:
        'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0',
      touchPoints: 0,
      want: 'deepcove-windows-edge',
    },
    {
      name: 'android phone samsung',
      userAgent:
        'Mozilla/5.0 (Linux; Android 14; SM-S921B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36',
      touchPoints: 5,
      want: 'deepcove-android-samsung-internet',
    },
    {
      name: 'android tablet chrome',
      userAgent:
        'Mozilla/5.0 (Linux; Android 14; SM-X710) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36',
      touchPoints: 5,
      want: 'deepcove-android-tablet-chrome',
    },
    {
      name: 'chromebook chrome',
      userAgent:
        'Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36',
      touchPoints: 0,
      want: 'deepcove-chromebook-chrome',
    },
    {
      name: 'linux opera',
      userAgent:
        'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 OPR/115.0.0.0',
      touchPoints: 0,
      want: 'deepcove-linux-opera',
    },
    {
      name: 'nothing known falls back',
      userAgent: 'curl/8.7.1',
      touchPoints: 0,
      want: 'deepcove-web',
    },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      expect(webNodeHostname(tc.userAgent, tc.touchPoints)).toBe(tc.want);
    });
  }
});
