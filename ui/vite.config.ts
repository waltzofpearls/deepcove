import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vite';

export default defineConfig({
  plugins: [tailwindcss(), sveltekit()],
  // The release this UI was built for. A node embeds it, and the web node is deployed from it.
  define: {
    __DEEPCOVE_VERSION__: JSON.stringify(process.env.DEEPCOVE_VERSION || 'dev'),
  },
});
