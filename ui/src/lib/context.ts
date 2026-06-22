// Svelte context keys shared across the layout and pages.

// NEW_SESSION_CTX holds a `() => void` that opens the New Session
// dialog (which is mounted in +layout.svelte). Pages read it to trigger
// the dialog without owning it, e.g. the dashboard's empty-state CTA.
export const NEW_SESSION_CTX = Symbol('open-new-session');
