<script lang="ts">
  // Official Tailscale "squircle" app-icon logo, taken verbatim from the Tailscale brand kit
  // (Tailscale_squircle): the dot mark inside a rounded square. variant 'dark' is the classic app icon
  // (near-black square, white dots); 'light' is the white-square version. Source: tailscale.com/press.
  let { class: className = '', variant = 'dark' }: { class?: string; variant?: 'dark' | 'light' } =
    $props();

  const dark = $derived(variant !== 'light');
  const bg = $derived(dark ? '#1F1E1E' : '#ffffff');
  const dot = $derived(dark ? '#ffffff' : '#1F1E1E');
  const faded = $derived(dark ? 0.4 : 0.3);
</script>

<svg viewBox="0 0 252 252" class={className} fill="none" aria-hidden="true">
  <rect width="252" height="252" rx="63" fill={bg} />
  <path d="M72 144C81.9411 144 90 135.941 90 126C90 116.059 81.9411 108 72 108C62.0589 108 54 116.059 54 126C54 135.941 62.0589 144 72 144Z" fill={dot} />
  <path d="M126 144C135.941 144 144 135.941 144 126C144 116.059 135.941 108 126 108C116.059 108 108 116.059 108 126C108 135.941 116.059 144 126 144Z" fill={dot} />
  <path d="M126 198C135.941 198 144 189.941 144 180C144 170.059 135.941 162 126 162C116.059 162 108 170.059 108 180C108 189.941 116.059 198 126 198Z" fill={dot} />
  <path d="M180 144C189.941 144 198 135.941 198 126C198 116.059 189.941 108 180 108C170.059 108 162 116.059 162 126C162 135.941 170.059 144 180 144Z" fill={dot} />
  <path d="M72 198C81.9411 198 90 189.941 90 180C90 170.059 81.9411 162 72 162C62.0589 162 54 170.059 54 180C54 189.941 62.0589 198 72 198Z" fill={dot} fill-opacity={faded} />
  <path d="M180 198C189.941 198 198 189.941 198 180C198 170.059 189.941 162 180 162C170.059 162 162 170.059 162 180C162 189.941 170.059 198 180 198Z" fill={dot} fill-opacity={faded} />
  <path d="M72 90C81.9411 90 90 81.9411 90 72C90 62.0589 81.9411 54 72 54C62.0589 54 54 62.0589 54 72C54 81.9411 62.0589 90 72 90Z" fill={dot} fill-opacity={faded} />
  <path d="M126 90C135.941 90 144 81.9411 144 72C144 62.0589 135.941 54 126 54C116.059 54 108 62.0589 108 72C108 81.9411 116.059 90 126 90Z" fill={dot} fill-opacity={faded} />
  <path d="M180 90C189.941 90 198 81.9411 198 72C198 62.0589 189.941 54 180 54C170.059 54 162 62.0589 162 72C162 81.9411 170.059 90 180 90Z" fill={dot} fill-opacity={faded} />
</svg>
