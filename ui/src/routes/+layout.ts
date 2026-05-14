// Disable SSR and prerendering everywhere. The deepcove UI is an
// embedded SPA served by the Go binary — there is no Node server to
// SSR from. Worse: code that runs in the browser only (the
// tailscale-web WASM client, anything that touches window /
// localStorage) will crash during prerender if SSR isn't off here.
//
// Setting both to false at the root +layout.ts cascades to every
// route, so individual pages don't need to repeat the dance.
export const ssr = false;
export const prerender = false;
