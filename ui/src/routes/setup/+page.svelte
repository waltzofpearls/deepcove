<script lang="ts">
  // First-run setup wizard (a full page, not an overlay). Tailscale-style: walk the user through
  // getting deepcove ready, including putting their two devices on the tailnet (session node required-
  // ish, web node optional/skippable). Steps: Welcome → Docker → Connect → Done. The whole thing is
  // skippable (deepcove works purely local), and finishing or skipping marks setup done (lib/setup).
  import { getContext } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api } from '$lib/api/client';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { connect } from '$lib/stores/connect.svelte';
  import { ensureTailnet, tailnetAuth } from '$lib/api/wasm';
  import { markSetupDone, setConnectSkipped } from '$lib/setup';
  import { openConnectingTab } from '$lib/signin';
  import { NEW_SESSION_CTX } from '$lib/context';
  import { Button } from '$lib/components/ui/button';
  import Logo from '$lib/components/layout/Logo.svelte';
  import TailscaleLogo from '$lib/components/TailscaleLogo.svelte';
  import Server from '@lucide/svelte/icons/server';
  import Globe from '@lucide/svelte/icons/globe';
  import Boxes from '@lucide/svelte/icons/boxes';
  import Check from '@lucide/svelte/icons/check';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';

  const steps = ['Welcome', 'Docker', 'Connect', 'Done'];
  // Deep link: the dashboard nudge sends ?step=connect to drop the user straight on the Connect step.
  let step = $state(page.url.searchParams.get('step') === 'connect' ? 2 : 0);

  $effect(() => preflight.subscribe());
  $effect(() => connect.subscribe());

  const dockerReady = $derived(preflight.loaded && preflight.dockerReachable);

  let auth = $derived($tailnetAuth);
  const webConnected = $derived(auth.phase === 'running');

  // Browsers only allow opening a tab during the click itself, not from an async callback a few seconds
  // later (that gets popup-blocked). So openConnectingTab opens the sign-in tab up front on the click
  // with a branded loading page, and we point it at the real login URL once that lands (from the backend
  // connect, or the lazily-loaded wasm client). The user sees a friendly loading tab, never blocked.
  let pendingSessionTab = $state<Window | null>(null);
  let pendingWebTab = $state<Window | null>(null);

  function connectSession() {
    pendingSessionTab = openConnectingTab();
    // Refresh status immediately so the row flips to "Starting…" now, not on the next poll tick.
    void api
      .connect()
      .then(() => connect.refresh())
      .catch(() => {});
  }
  function signInWeb() {
    pendingWebTab = openConnectingTab();
    void ensureTailnet().catch(() => {});
  }

  // Session node: send the pre-opened tab to the backend's login URL once it surfaces; if it connected
  // outright (cached creds), close the spare tab.
  $effect(() => {
    if (!pendingSessionTab) return;
    const url = connect.status?.login_url;
    if (url) {
      pendingSessionTab.location.href = url;
      pendingSessionTab = null;
    } else if (connect.connected) {
      pendingSessionTab.close();
      pendingSessionTab = null;
    }
  });

  // Web node: same handoff, driven by the in-browser node's auth phase.
  $effect(() => {
    if (!pendingWebTab) return;
    if (auth.phase === 'needs_login' && auth.url) {
      pendingWebTab.location.href = auth.url;
      pendingWebTab = null;
    } else if (auth.phase === 'running' || auth.phase === 'error') {
      pendingWebTab.close();
      pendingWebTab = null;
    }
  });

  // The New Session dialog lives in the layout (not mounted on this page), but the layout's open
  // callback is set at the top level via context, so it survives the navigation. Flip it open before
  // leaving, then the dialog is already open when the dashboard chrome mounts.
  const openNewSession = getContext<() => void>(NEW_SESSION_CTX);

  function finish(createSession: boolean) {
    markSetupDone();
    // Remember if they left the session node unconnected, so the dashboard can nudge them to finish it.
    setConnectSkipped(!connect.connected);
    if (createSession) openNewSession();
    void goto('/');
  }
</script>

{#snippet skipLink()}
  <button
    type="button"
    onclick={() => finish(false)}
    class="cursor-pointer text-sm text-muted-foreground underline underline-offset-4 transition-colors hover:text-foreground"
  >
    Skip setup
  </button>
{/snippet}

<div class="flex min-h-screen flex-col items-center bg-background px-6 pt-12 pb-16 text-foreground">
  <div class="w-full max-w-2xl">
    <div class="mb-12 mt-6 flex justify-center">
      <Logo class="h-14 w-auto" />
    </div>

    <!-- Step progress -->
    <div class="mb-8 flex items-center gap-2">
      {#each steps as label, i (label)}
        <div class="flex flex-1 flex-col gap-1.5">
          <div
            class="h-1 rounded-full transition-colors"
            class:bg-primary={i <= step}
            class:bg-secondary={i > step}
          ></div>
          <span class="text-[10px] {i === step ? 'text-foreground' : 'text-muted-foreground'}">
            {label}
          </span>
        </div>
      {/each}
    </div>

    <div class="rounded-xl border border-border bg-card p-6">
      {#if step === 0}
        <div class="flex flex-col items-center gap-4 py-4 text-center">
          <div class="flex size-14 items-center justify-center rounded-2xl bg-primary/10 text-primary">
            <Boxes class="size-7" />
          </div>
          <h1 class="text-xl font-semibold">Welcome to deepcove</h1>
          <p class="max-w-sm text-sm text-muted-foreground">
            Run coding agents like Claude Code in isolated sandboxes on your own machines, reachable
            from anywhere on your tailnet. Let's get you set up. It only takes a minute.
          </p>
          <Button class="mt-2" onclick={() => (step = 1)}>Get started</Button>
        </div>
      {:else if step === 1}
        <div class="space-y-4">
          <h2 class="text-lg font-semibold">Check Docker</h2>
          <p class="text-sm text-muted-foreground">
            Each session runs in its own Docker container, so deepcove needs Docker running on this
            machine.
          </p>
          {#if dockerReady}
            <div class="flex items-center gap-2 rounded-md border border-border bg-secondary/30 px-3 py-2.5 text-sm">
              <Check class="size-4 text-[var(--color-success)]" /> Docker is running.
            </div>
          {:else if preflight.loaded}
            <div class="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-sm">
              <CircleAlert class="size-4 shrink-0 translate-y-0.5 text-destructive" />
              <div>
                {preflight.docker.message || "Docker isn't reachable. Start Docker and try again."}
                This will update automatically once Docker is running.
              </div>
            </div>
          {:else}
            <div class="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 class="size-4 animate-spin" /> Checking…
            </div>
          {/if}
          <div class="flex items-center justify-between pt-2">
            <Button variant="ghost" onclick={() => (step = 0)}>Back</Button>
            <div class="flex items-center gap-4">
              {@render skipLink()}
              <Button onclick={() => (step = 2)} disabled={!dockerReady}>Continue</Button>
            </div>
          </div>
        </div>
      {:else if step === 2}
        <div class="space-y-4">
          <div class="flex items-center gap-2">
            <h2 class="text-lg font-semibold">Connect to Tailscale</h2>
            <span class="rounded bg-secondary px-1.5 py-0.5 text-[10px] font-normal text-muted-foreground">
              optional
            </span>
          </div>
          <p class="text-sm text-muted-foreground">
            Only needed to reach your sessions remotely or run them across more than one machine. If
            you'll just use sessions on this machine, skip this and connect anytime later.
          </p>

          <!-- Device 1: session node (this backend). -->
          <div class="flex items-center gap-3 rounded-lg border border-border p-3">
            <Server class="size-5 shrink-0 text-muted-foreground" />
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium">Session node</div>
              <div class="text-xs text-muted-foreground">This machine, runs your sessions</div>
            </div>
            {#if connect.connected}
              <span class="flex shrink-0 items-center gap-1 text-sm text-[var(--color-success)]">
                <Check class="size-4" /> Connected
              </span>
            {:else if connect.status?.login_url}
              <Button class="shrink-0" href={connect.status.login_url} target="_blank" rel="noopener noreferrer">
                <TailscaleLogo class="size-5 -ml-0.5" /> Sign in
              </Button>
            {:else if connect.pending}
              <span class="flex shrink-0 items-center gap-2 text-sm text-muted-foreground">
                <Loader2 class="size-4 animate-spin" /> Starting…
              </span>
            {:else}
              <Button class="shrink-0" onclick={connectSession}>
                <TailscaleLogo class="size-5 -ml-0.5" /> Sign in
              </Button>
            {/if}
          </div>

          <!-- Device 2: web node (this browser). -->
          <div class="flex items-center gap-3 rounded-lg border border-border p-3">
            <Globe class="size-5 shrink-0 text-muted-foreground" />
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium">Web node</div>
              <div class="text-xs text-muted-foreground">
                This browser, reaches sessions on other machines
              </div>
            </div>
            {#if webConnected}
              <span class="flex shrink-0 items-center gap-1 text-sm text-[var(--color-success)]">
                <Check class="size-4" /> Connected
              </span>
            {:else if auth.phase === 'needs_login'}
              <Button class="shrink-0" href={auth.url} target="_blank" rel="noopener noreferrer">
                <TailscaleLogo class="size-5 -ml-0.5" /> Sign in
              </Button>
            {:else if auth.phase === 'connecting'}
              <span class="flex shrink-0 items-center gap-2 text-sm text-muted-foreground">
                <Loader2 class="size-4 animate-spin" /> Connecting…
              </span>
            {:else}
              <Button class="shrink-0" onclick={signInWeb}>
                <TailscaleLogo class="size-5 -ml-0.5" /> Sign in
              </Button>
            {/if}
          </div>

          <div class="flex items-center justify-between pt-2">
            <Button variant="ghost" onclick={() => (step = 1)}>Back</Button>
            <div class="flex items-center gap-4">
              {@render skipLink()}
              <Button onclick={() => (step = 3)}>Continue</Button>
            </div>
          </div>
        </div>
      {:else}
        <div class="flex flex-col items-center gap-4 py-4 text-center">
          <div class="flex size-14 items-center justify-center rounded-2xl bg-[var(--color-success)]/10 text-[var(--color-success)]">
            <Check class="size-7" />
          </div>
          <h2 class="text-xl font-semibold">You're all set</h2>
          <p class="max-w-sm text-sm text-muted-foreground">
            deepcove is ready. Create your first session to run a coding agent in its own sandbox.
          </p>
          <div class="mt-2 flex gap-2">
            <Button variant="outline" onclick={() => finish(false)}>Go to dashboard</Button>
            <Button onclick={() => finish(true)}>Create your first session</Button>
          </div>
        </div>
      {/if}
    </div>
  </div>
</div>
