<script lang="ts">
  // First-run setup wizard (a full page, not an overlay). Tailscale-style: walk the user through
  // getting deepcove ready, including putting their two devices on the tailnet (session node required-
  // ish, web node optional/skippable). Steps: Welcome → Virtual machines → Workspace (skipped without a
  // VM) → Connect → Done. The whole thing is skippable (deepcove works purely local), and finishing or
  // skipping marks setup done (lib/setup).
  import { getContext } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { api } from '$lib/api/client';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { connect } from '$lib/stores/connect.svelte';
  import { ensureTailnet, tailnetAuth } from '$lib/api/wasm';
  import { isSetupDone, markSetupDone, setConnectSkipped } from '$lib/setup';
  import { openConnectingTab } from '$lib/signin';
  import { NEW_SESSION_CTX } from '$lib/context';
  import {
    bootsVMs,
    runtimeLabel,
    runtimeMissingReason,
    runtimeSetUpByDeepcove,
  } from '$lib/util/runtime';
  import RuntimeSetupButton from '$lib/components/RuntimeSetupButton.svelte';
  import WorkspaceVMFields from '$lib/components/session/WorkspaceVMFields.svelte';
  import { looksLikePath } from '$lib/util/path';
  import { DEFAULT_VM_SIZE } from '$lib/util/vmSize';
  import { Button } from '$lib/components/ui/button';
  import Logo from '$lib/components/layout/Logo.svelte';
  import TailscaleLogo from '$lib/components/TailscaleLogo.svelte';
  import Server from '@lucide/svelte/icons/server';
  import Globe from '@lucide/svelte/icons/globe';
  import Boxes from '@lucide/svelte/icons/boxes';
  import Check from '@lucide/svelte/icons/check';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import CircleAlert from '@lucide/svelte/icons/circle-alert';

  const steps = ['Welcome', 'Virtual machines', 'Workspace', 'Connect', 'Done'];
  // Deep links: ?step=vm opens the virtual machine step (the "nothing can host a session" banner and the
  // dialog send you there), ?step=connect the Connect step (the dashboard nudge).
  const deepLinkedSteps: Record<string, number> = { vm: 1, connect: 3 };
  const deepLink = page.url.searchParams.get('step') ?? '';
  let step = $state(deepLinkedSteps[deepLink] ?? 0);
  // Sent here to set up virtual machines and nothing else, so that step ends back home.
  const cameToSetUpVMs = deepLink === 'vm';
  // The VM step starts its download on its own only when you came to set it up: "Get started", or a
  // Set up link in deepcove (banner, dialog), which passes page state. Never an outside link, never Back.
  let cameForward = $state(false);
  const autostartSetup = $derived(cameForward || page.state.autostartSetup === true);
  // The workspace step builds a shared VM, which a machine without a VM runtime cannot, so it is
  // skipped there both ways.
  const hasVMRuntime = $derived((preflight.runtimes?.available ?? []).some(bootsVMs));

  $effect(() => preflight.subscribe());
  $effect(() => connect.subscribe());

  // The workspace VM, offered here so a first run can set one up before creating anything. Skippable:
  // without one, every session gets a VM of its own, which is how deepcove worked before.
  let workspaceHostRoot = $state('');
  let workspaceGuestRoot = $state('/workspace');
  let workspaceSize = $state(DEFAULT_VM_SIZE);
  let workspaceError = $state<string | null>(null);
  let savingWorkspace = $state(false);

  async function createWorkspaceVM() {
    if (savingWorkspace) return;
    savingWorkspace = true;
    workspaceError = null;
    try {
      await api.saveWorkspaceVM({
        host_root: workspaceHostRoot.trim(),
        guest_root: workspaceGuestRoot.trim() || undefined,
        resources: { size: workspaceSize },
      });
      step = 3;
    } catch (e) {
      workspaceError = e instanceof Error ? e.message : String(e);
    } finally {
      savingWorkspace = false;
    }
  }

  const runtimesReady = $derived(preflight.loaded && preflight.usableRuntimes.length > 0);
  // This step is about virtual machines only: running with no sandbox is a choice made in the New
  // Session dialog, never offered here. A missing VM runtime deepcove can install is set up on arrival.
  const vmReady = $derived(preflight.usableRuntimes.some(bootsVMs));
  const vmMissing = $derived(
    Object.entries(preflight.runtimes?.missing ?? {}).filter(([name]) => bootsVMs(name)),
  );
  const vmDown = $derived(
    Object.entries(preflight.runtimes?.unavailable ?? {}).filter(([name]) => bootsVMs(name)),
  );
  let settingUpVM = $state(false);

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
    // Whether this was onboarding, not a revisit. Read before markSetupDone flips it.
    const firstRun = !isSetupDone();
    markSetupDone();
    // Nudge only after onboarding. Re-opening the wizard from the menu would otherwise bring back a
    // nudge the user already dismissed.
    if (firstRun) setConnectSkipped(!connect.connected);
    if (createSession) openNewSession();
    void goto('/');
  }
</script>

{#snippet skipLink()}
  <button
    type="button"
    onclick={() => finish(false)}
    disabled={settingUpVM}
    class="cursor-pointer text-sm text-muted-foreground underline underline-offset-4 transition-colors hover:text-foreground disabled:cursor-not-allowed disabled:opacity-50"
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
          <div
            class="flex size-14 items-center justify-center rounded-2xl bg-primary/10 text-primary"
          >
            <Boxes class="size-7" />
          </div>
          <h1 class="text-xl font-semibold">Welcome to deepcove</h1>
          <p class="max-w-sm text-sm text-muted-foreground">
            Run coding agents like Claude Code in isolated sandboxes on your own machines, reachable
            from anywhere on your tailnet. Let's get you set up. It only takes a minute.
          </p>
          <Button
            class="mt-2"
            onclick={() => {
              cameForward = true;
              step = 1;
            }}
          >
            Get started
          </Button>
        </div>
      {:else if step === 1}
        <div class="space-y-4">
          <h2 class="text-lg font-semibold">Set up virtual machines</h2>
          <p class="text-sm text-muted-foreground">
            {vmReady
              ? 'Each session runs in a virtual machine, so the agent cannot touch the rest of this machine.'
              : 'A virtual machine keeps each session from touching the rest of this machine.'}
          </p>
          {#if !preflight.loaded}
            <div class="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 class="size-4 animate-spin" /> Checking…
            </div>
          {:else if vmReady}
            <div
              class="flex items-center gap-2 rounded-md border border-border bg-secondary/30 px-3 py-2.5 text-sm"
            >
              <Check class="size-4 text-[var(--color-success)]" />
              Virtual machines are ready.
            </div>
          {:else}
            {#each vmMissing as [name, code] (name)}
              <div
                class="flex items-start gap-2 rounded-md border border-border px-3 py-2.5 text-sm"
              >
                <Boxes class="size-4 shrink-0 translate-y-0.5 text-muted-foreground" />
                <div class="flex-1">
                  {#if runtimeSetUpByDeepcove(code)}
                    <p class="mb-2">
                      {settingUpVM
                        ? 'Setting up virtual machines. This downloads once.'
                        : 'Virtual machines need a one-time download.'}
                    </p>
                    <RuntimeSetupButton
                      runtime={name}
                      autostart={autostartSetup}
                      bind:busy={settingUpVM}
                      ondone={() => preflight.refresh()}
                    />
                  {:else}
                    <p>{runtimeMissingReason(code)}</p>
                  {/if}
                </div>
              </div>
            {/each}
            {#each vmDown as [name, why] (name)}
              <div
                class="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-sm"
              >
                <CircleAlert class="size-4 shrink-0 translate-y-0.5 text-destructive" />
                <p>{runtimeLabel(name)}: {why}</p>
              </div>
            {/each}
            {#if vmMissing.length === 0 && vmDown.length === 0}
              <div
                class="flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2.5 text-sm"
              >
                <CircleAlert class="size-4 shrink-0 translate-y-0.5 text-destructive" />
                <p>This machine cannot run virtual machines.</p>
              </div>
            {/if}
          {/if}
          <div class="flex items-center justify-between pt-2">
            <!-- Sent here to fix the VM and nothing else, so there is no wizard to go back through. -->
            {#if !cameToSetUpVMs}
              <Button variant="ghost" onclick={() => (step = 0)} disabled={settingUpVM}>
                Back
              </Button>
            {/if}
            <div class="ml-auto flex items-center gap-4">
              {#if cameToSetUpVMs}
                <Button onclick={() => goto('/')} disabled={settingUpVM}>Back to dashboard</Button>
              {:else}
                {@render skipLink()}
                <Button
                  onclick={() => (step = hasVMRuntime ? 2 : 3)}
                  disabled={!runtimesReady || settingUpVM}
                >
                  Continue
                </Button>
              {/if}
            </div>
          </div>
        </div>
      {:else if step === 2}
        <div class="space-y-4">
          <h2 class="text-lg font-semibold">Share a folder with your sessions</h2>
          <p class="text-sm text-muted-foreground">
            One VM your sessions share, each working in its own folder inside the folder you pick.
            Skip this and every session gets a VM of its own instead.
          </p>
          <WorkspaceVMFields
            bind:hostRoot={workspaceHostRoot}
            bind:guestRoot={workspaceGuestRoot}
            bind:size={workspaceSize}
          />
          {#if workspaceError}
            <p class="text-sm text-destructive">{workspaceError}</p>
          {/if}
          <div class="flex items-center justify-between pt-2">
            <Button variant="ghost" onclick={() => ((cameForward = false), (step = 1))}>
              Back
            </Button>
            <div class="flex items-center gap-4">
              <button
                type="button"
                onclick={() => (step = 3)}
                class="cursor-pointer text-sm text-muted-foreground underline underline-offset-4 transition-colors hover:text-foreground"
              >
                Skip this
              </button>
              <Button
                onclick={createWorkspaceVM}
                disabled={savingWorkspace || !looksLikePath(workspaceHostRoot)}
              >
                {savingWorkspace ? 'Creating…' : 'Create it'}
              </Button>
            </div>
          </div>
        </div>
      {:else if step === 3}
        <div class="space-y-4">
          <div class="flex items-center gap-2">
            <h2 class="text-lg font-semibold">Connect to Tailscale</h2>
            <span
              class="rounded bg-secondary px-1.5 py-0.5 text-[10px] font-normal text-muted-foreground"
            >
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
              <Button
                class="shrink-0"
                href={connect.status.login_url}
                target="_blank"
                rel="noopener noreferrer"
              >
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
            <Button
              variant="ghost"
              onclick={() => ((cameForward = false), (step = hasVMRuntime ? 2 : 1))}
            >
              Back
            </Button>
            <div class="flex items-center gap-4">
              {@render skipLink()}
              <Button onclick={() => (step = 4)}>Continue</Button>
            </div>
          </div>
        </div>
      {:else}
        <div class="flex flex-col items-center gap-4 py-4 text-center">
          <div
            class="flex size-14 items-center justify-center rounded-2xl bg-[var(--color-success)]/10 text-[var(--color-success)]"
          >
            <Check class="size-7" />
          </div>
          <h2 class="text-xl font-semibold">You're all set</h2>
          <p class="max-w-sm text-sm text-muted-foreground">
            deepcove is ready. Create your first session to run a coding agent in a sandbox.
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
