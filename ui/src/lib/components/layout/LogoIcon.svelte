<script lang="ts">
  // deepcove icon logo, the solid brand-teal hexagon, no text (same mark as
  // the favicon). Fill uses the brand-teal token var(--primary), so it tracks
  // the theme (#009d89 light / #00aa95 dark) and matches the UI accent.
  // Decorative, so aria-hidden where it sits next to a text label. For the full
  // branding logo (with the "deepcove" letters) use Logo.svelte.
  let { class: className = 'size-4' }: { class?: string } = $props();
</script>

<svg class={className} viewBox="0 0 100 100" xmlns="http://www.w3.org/2000/svg" aria-hidden="true">
  <polygon class="fill-primary" points="50,4 89.84,27 89.84,73 50,96 10.16,73 10.16,27" />
</svg>
