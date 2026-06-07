import { defineConfig } from 'vitest/config';

// Standalone from vite.config.ts (the SvelteKit plugin isn't needed for these
// pure-logic unit tests, and keeping it out avoids kit/SSR setup in the runner).
// Tests are colocated next to source as *.test.ts and use relative imports, so no
// $lib alias or svelte compilation is required. node environment — no DOM.
export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
    coverage: {
      provider: 'v8',
      // Report on the logic we actually unit-test (utils + api), not .svelte
      // components/routes (those aren't covered by these node-env tests).
      include: ['src/lib/util/**/*.ts', 'src/lib/api/**/*.ts'],
      reporter: ['text', 'text-summary'],
    },
  },
});
