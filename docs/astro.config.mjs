import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLlmsTxt from 'starlight-llms-txt';

export default defineConfig({
  site: 'https://docs.deepcove.dev',
  outDir: './dist',
  // The logos and the favicon come from the app, so the docs never carry a second copy that drifts.
  publicDir: '../ui/static',
  integrations: [
    starlight({
      title: 'deepcove docs',
      description: 'Run coding agents in their own VMs, across all your machines.',
      logo: {
        light: '../ui/static/deepcove-logo-light.svg',
        dark: '../ui/static/deepcove-logo-dark.svg',
        alt: 'deepcove docs',
        replacesTitle: true,
      },
      favicon: '/favicon.svg',
      customCss: ['./src/styles/theme.css'],
      // Code boxes match the dashboard's terminal: one plain box with a copy button, no window frame.
      expressiveCode: {
        defaultProps: { frame: 'code' },
        styleOverrides: {
          borderRadius: '0.625rem',
          borderColor: 'var(--sl-color-hairline)',
          codeBackground: 'var(--deepcove-code-bg)',
          frames: { shadowColor: 'transparent', editorBackground: 'var(--deepcove-code-bg)' },
        },
      },
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/waltzofpearls/deepcove' },
      ],
      editLink: { baseUrl: 'https://github.com/waltzofpearls/deepcove/edit/main/docs/' },
      plugins: [starlightLlmsTxt({ projectName: 'deepcove' })],
      sidebar: [
        {
          label: 'Get started',
          items: ['index', 'get-started/install', 'get-started/first-session'],
        },
        { label: 'Use it', items: ['use-it/connect', 'use-it/add-a-machine', 'use-it/mobile'] },
        { label: 'Sessions', items: ['sessions/tabs', 'sessions/upload', 'sessions/search'] },
        {
          label: 'Where sessions run',
          items: [
            'where-sessions-run/vm-on-macos',
            'where-sessions-run/vm-on-linux',
            'where-sessions-run/no-sandbox',
          ],
        },
        { label: 'Reference', items: ['reference/cli', 'reference/settings'] },
      ],
    }),
  ],
});
