<script lang="ts">
  // Route-level error boundary. SvelteKit renders this when a load
  // function throws or a navigation hits an HTTP error, instead of a
  // blank screen. Keep it calm and offer a way back.
  import { page } from '$app/state';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
</script>

<div
  class="flex h-screen flex-col items-center justify-center gap-4 bg-background p-8 text-foreground"
>
  <TriangleAlert class="size-10 text-destructive" />
  <div class="text-center">
    <h1 class="text-lg font-semibold">
      {page.status}
      {page.status === 404 ? '— page not found' : '— something went wrong'}
    </h1>
    <p class="mt-1 max-w-md text-sm text-muted-foreground">
      {page.error?.message ?? 'An unexpected error occurred.'}
    </p>
  </div>
  <a
    href="/"
    class="inline-flex h-9 items-center rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
  >
    Back to dashboard
  </a>
</div>
