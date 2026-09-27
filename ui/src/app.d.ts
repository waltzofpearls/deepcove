// See https://svelte.dev/docs/kit/types#app.d.ts
// for information about these interfaces
declare global {
  namespace App {
    // interface Error {}
    // interface Locals {}
    // interface PageData {}
    interface PageState {
      // Set by deepcove's own links into setup, never by an outside one, so only those start a download.
      autostartSetup?: boolean;
    }
    // interface Platform {}
  }
}

export {};
