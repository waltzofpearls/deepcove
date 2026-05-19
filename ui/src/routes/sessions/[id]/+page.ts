// xterm.js touches `window`, and the adapter-static prerender step
// runs in Node. Force this route to client-only so prerender doesn't
// trip over the WebGL renderer initialisation.
export const ssr = false;
export const prerender = false;
