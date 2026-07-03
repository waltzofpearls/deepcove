// Shared sign-in helper for putting deepcove on the tailnet. Both the setup wizard and the device bar
// open the Tailscale login tab the same way: opened during the click (so the browser does not
// popup-block it), showing a branded "Connecting" page, then navigated to the real login URL once it
// lands. Keeping this in one place is what keeps the two surfaces consistent.

// Standalone HTML rendered into the freshly-opened sign-in tab while we wait for the login URL, so it
// shows a branded loading state rather than a blank page. It has no access to the app's CSS, so the
// styling is inline; it follows the OS light/dark preference.
const CONNECTING_PAGE = `<!doctype html><html><head><meta charset="utf-8"><title>Connecting to Tailscale…</title><style>html,body{height:100%;margin:0}body{display:flex;align-items:center;justify-content:center;font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;background:#fff;color:#16181d}@media(prefers-color-scheme:dark){body{background:#16181d;color:#e6e6e6}}.s{width:28px;height:28px;margin:0 auto 16px;border:3px solid rgba(127,127,127,.25);border-top-color:#00aa95;border-radius:50%;animation:r .8s linear infinite}@keyframes r{to{transform:rotate(360deg)}}p{font-size:14px;opacity:.75;text-align:center}</style></head><body><div><div class="s"></div><p>Connecting to Tailscale…</p></div></body></html>`;

// openConnectingTab opens a blank tab during the click and fills it with the loading page above. Hold
// onto the returned Window and set its location to the login URL once it is known (or close it if the
// node connected without needing one). Returns null in a non-browser context or if the open was blocked.
export function openConnectingTab(): Window | null {
	if (typeof window === 'undefined') return null;
	const tab = window.open('', '_blank');
	if (tab) {
		tab.document.write(CONNECTING_PAGE);
		tab.document.close();
	}
	return tab;
}
