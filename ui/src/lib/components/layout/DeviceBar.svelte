<script lang="ts">
  // Makes deepcove's two-device model explicit in the topbar, so signing in is obvious instead of
  // hidden. Each device on your tailnet gets a chip:
  //   - Session node: the backend that runs your sessions (this machine's tsnet connection).
  //   - Web node:     your browser as its own tailnet device, used to reach session nodes' streams.
  // On a session node (direct boot) we show both; on a dashboard thin client (wasm) only the web node.
  // Not signed in → the chip shows the type label and clicking opens a sign-in dialog. Signed in → it
  // shows the node's name with the type as a footnote, and clicking opens an info dialog.
  import { transport } from '$lib/api/transport';
  import { connect } from '$lib/stores/connect.svelte';
  import { api } from '$lib/api/client';
  import {
    ensureTailnet,
    tailnetAuth,
    webNodeInfo,
    wasmHostname,
    clearWebNodeSignIn,
    type WebNodeInfo,
  } from '$lib/api/wasm';
  import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogDescription,
  } from '$lib/components/ui/dialog';
  import {
    Tooltip,
    TooltipProvider,
    TooltipTrigger,
    TooltipContent,
  } from '$lib/components/ui/tooltip';
  import {
    AlertDialog,
    AlertDialogContent,
    AlertDialogHeader,
    AlertDialogTitle,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogCancel,
    AlertDialogAction,
  } from '$lib/components/ui/alert-dialog';
  import { Button } from '$lib/components/ui/button';
  import { openConnectingTab } from '$lib/signin';
  import { machineName } from '$lib/util/tailnet';
  import TailscaleLogo from '$lib/components/TailscaleLogo.svelte';
  import Server from '@lucide/svelte/icons/server';
  import Globe from '@lucide/svelte/icons/globe';
  import Info from '@lucide/svelte/icons/info';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import Loader2 from '@lucide/svelte/icons/loader-2';

  // direct boot = a real backend node is serving this UI (a session node). wasm boot = dashboard thin
  // client, where the only tailnet device is the browser's web node.
  const isSessionNode = transport.mode === 'direct';

  $effect(() => {
    if (isSessionNode) return connect.subscribe();
  });

  // Session node (this backend's tailnet connection, from /api/connect/status).
  const sessionConnected = $derived(connect.connected);
  // Orange means "actively coming up" (transient). A node that's stopped, or started but still waiting
  // for sign-in, reads as not-connected (grey) so an incomplete connect never looks perpetually busy.
  const sessionConnecting = $derived(connect.status?.phase === 'starting');
  const sessionName = $derived(connect.status?.name ?? '');
  // status.name is the full MagicDNS name (deepcove-host-5.tailnet.ts.net); the chip/title show just the
  // machine name (first label) to match the Tailscale admin, and the full name lives in a dialog field.
  const sessionShortName = $derived(machineName(sessionName));

  // Web node (the in-browser Tailscale node).
  let auth = $derived($tailnetAuth);
  const webConnected = $derived(auth.phase === 'running');
  const webConnecting = $derived(auth.phase === 'connecting');

  let openDevice = $state<'session' | 'web' | null>(null);

  // When nothing is on the tailnet yet, collapse both chips into a single "Connect to Tailscale" CTA
  // (only on a session node, where both devices exist). Clicking it reveals the two node buttons so the
  // user can sign each in. Once either is connected or in progress, the chips stay shown.
  let expanded = $state(false);
  const collapsed = $derived(
    isSessionNode &&
      !expanded &&
      !sessionConnected &&
      !webConnected &&
      !sessionConnecting &&
      !webConnecting,
  );

  // Live web-node details. Loaded whenever the web node is connected (not just while the dialog is
  // open) so the chip can show the real machine name, which the static hostname can't (dedup suffix).
  let webInfo = $state<WebNodeInfo | null>(null);
  $effect(() => {
    if (webConnected) {
      webNodeInfo()
        .then((i) => (webInfo = i))
        .catch(() => {});
    } else {
      webInfo = null;
    }
  });

  // Sign-in opens the Tailscale login tab during the click (so it is not popup-blocked) with a branded
  // loading page, then we point it at the real login URL once it lands. Identical flow to the wizard.
  let pendingSessionTab = $state<Window | null>(null);
  let pendingWebTab = $state<Window | null>(null);

  function connectSession() {
    pendingSessionTab = openConnectingTab();
    // Refresh status immediately so the dialog flips to "Connecting…" now, not on the next poll tick.
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

  // Web node "sign out": tailscale-web has no logout/shutdown, so the running node stays up until the
  // tab closes. The most we can do without a reload is drop its saved credentials, so the next session
  // signs in fresh. Honestly labeled in the dialog.
  let webCleared = $state(false);
  let clearOpen = $state(false);
  function clearWebSignIn() {
    clearWebNodeSignIn();
    webCleared = true;
    clearOpen = false;
  }

  // Session node management (mirrors the Settings panel): disconnect keeps tsnet state for a quick
  // reconnect; forget logs the node out of the tailnet and wipes state, so the next sign-in is fresh.
  let actionBusy = $state(false);
  let actionLabel = $state('');
  let copied = $state(false);

  async function copySessionUrl() {
    const url = connect.status?.url;
    if (!url) return;
    try {
      await navigator.clipboard.writeText(url);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      // Clipboard needs a secure context; the link stays selectable as a fallback.
    }
  }

  async function disconnectSession() {
    actionLabel = 'Disconnecting…';
    actionBusy = true;
    try {
      await api.disconnect();
      await connect.refresh();
      openDevice = null;
    } catch {
      // Leave the dialog open; the poll will reflect whatever state it settled in.
    } finally {
      actionBusy = false;
    }
  }

  let forgetOpen = $state(false);
  async function forgetSession() {
    actionLabel = 'Forgetting…';
    actionBusy = true;
    try {
      await api.resetConnect();
      await connect.refresh();
      openDevice = null;
    } catch {
      // Same as disconnect: keep the dialog; the poll reflects the result.
    } finally {
      actionBusy = false;
      forgetOpen = false;
    }
  }

  function dotClass(connected: boolean, pending: boolean): string {
    if (connected) return 'bg-green-500';
    if (pending) return 'bg-amber-500';
    return 'bg-muted-foreground';
  }
</script>

<div class="flex items-center gap-2">
  {#if collapsed}
    <!-- Single call to action while nothing is connected; the (i) tooltip explains why you'd bother. -->
    <TooltipProvider delayDuration={200}>
      <Tooltip>
        <TooltipTrigger>
          {#snippet child({ props })}
            <button
              {...props}
              type="button"
              onclick={() => (expanded = true)}
              class="inline-flex h-10 items-center gap-2 rounded-md bg-primary px-3 text-xs font-medium text-primary-foreground shadow-xs transition-opacity hover:opacity-90"
            >
              <TailscaleLogo class="size-4 shrink-0" />
              <span class="hidden sm:inline">Connect to Tailscale</span>
              <Info class="size-3.5 shrink-0 opacity-80" />
            </button>
          {/snippet}
        </TooltipTrigger>
        <TooltipContent class="max-w-xs text-center">
          Put deepcove on your Tailscale network to reach your sessions from anywhere, and to run
          them across more than one machine. It's optional, since sessions on this machine work
          without it.
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  {:else}
    {#if isSessionNode}
      <button
        type="button"
        onclick={() => (openDevice = 'session')}
        class="flex h-10 items-center gap-2 rounded-md border border-border bg-card px-2.5 text-left transition-colors hover:bg-secondary"
        aria-label="Session node"
      >
        <Server class="size-4 shrink-0 text-muted-foreground" />
        <div class="hidden leading-tight sm:block">
          {#if sessionConnected && sessionShortName}
            <div class="text-xs font-medium">{sessionShortName}</div>
            <div class="text-[10px] text-muted-foreground">Session node</div>
          {:else if sessionConnecting}
            <div class="text-xs font-medium text-muted-foreground">Session node</div>
            <div class="text-[10px] text-muted-foreground">Connecting…</div>
          {:else}
            <div class="text-xs font-medium text-muted-foreground">Session node</div>
          {/if}
        </div>
        <span class="size-2 shrink-0 rounded-full {dotClass(sessionConnected, sessionConnecting)}"
        ></span>
      </button>
    {/if}

    <button
      type="button"
      onclick={() => (openDevice = 'web')}
      class="flex h-10 items-center gap-2 rounded-md border border-border bg-card px-2.5 text-left transition-colors hover:bg-secondary"
      aria-label="Web node"
    >
      <Globe class="size-4 shrink-0 text-muted-foreground" />
      <div class="hidden leading-tight sm:block">
        {#if webConnected}
          <div class="text-xs font-medium">{webInfo?.name ?? wasmHostname()}</div>
          <div class="text-[10px] text-muted-foreground">Web node</div>
        {:else if webConnecting}
          <div class="text-xs font-medium text-muted-foreground">Web node</div>
          <div class="text-[10px] text-muted-foreground">Connecting…</div>
        {:else}
          <div class="text-xs font-medium text-muted-foreground">Web node</div>
        {/if}
      </div>
      <span class="size-2 shrink-0 rounded-full {dotClass(webConnected, webConnecting)}"></span>
    </button>
  {/if}
</div>

<Dialog open={openDevice !== null} onOpenChange={(o) => !o && (openDevice = null)}>
  <DialogContent class="gap-5 p-6 sm:max-w-2xl">
    {#if openDevice === 'session'}
      {#if sessionConnected}
        <DialogHeader>
          <DialogTitle class="font-mono text-xl">{sessionShortName || 'Session node'}</DialogTitle>
          <DialogDescription
            >The backend that runs your sessions, on your tailnet.</DialogDescription
          >
        </DialogHeader>
        <dl class="space-y-3 text-sm">
          <div class="flex justify-between gap-4">
            <dt class="text-muted-foreground">Type</dt>
            <dd>Session node</dd>
          </div>
          <div class="flex justify-between gap-4">
            <dt class="text-muted-foreground">Status</dt>
            <dd class="text-green-500">Connected</dd>
          </div>
          {#if connect.ownerName}
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Signed in as</dt>
              <dd class="truncate">{connect.ownerName}</dd>
            </div>
          {/if}
          {#if connect.status?.tailnet_name}
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Tailnet</dt>
              <dd class="truncate font-mono text-xs">{connect.status.tailnet_name}</dd>
            </div>
          {/if}
          {#if connect.status?.name}
            <div class="flex justify-between gap-4">
              <dt class="shrink-0 text-muted-foreground">MagicDNS name</dt>
              <dd class="truncate font-mono text-xs">{connect.status.name}</dd>
            </div>
          {/if}
          {#if connect.status?.ips?.length}
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Addresses</dt>
              <dd class="text-right font-mono text-xs">
                {#each connect.status.ips as ip (ip)}<div>{ip}</div>{/each}
              </dd>
            </div>
          {/if}
        </dl>
        {#if connect.status?.url}
          <div
            class="flex items-center gap-2 rounded-md border border-border bg-secondary p-2 font-mono text-xs"
          >
            <a
              href={connect.status.url}
              target="_blank"
              rel="noopener"
              class="flex-1 truncate hover:underline">{connect.status.url}</a
            >
            <Button
              size="icon"
              variant="ghost"
              class="size-7 shrink-0"
              onclick={copySessionUrl}
              aria-label="Copy URL"
            >
              {#if copied}
                <Check class="size-3.5 text-green-500" />
              {:else}
                <Copy class="size-3.5" />
              {/if}
            </Button>
          </div>
        {/if}
        <div class="flex items-center gap-3 pt-1">
          {#if actionBusy}
            <div class="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 class="size-4 animate-spin" />
              {actionLabel}
            </div>
          {:else}
            <Button size="sm" variant="outline" onclick={disconnectSession}>Disconnect</Button>
            <button
              type="button"
              onclick={() => (forgetOpen = true)}
              class="text-xs text-muted-foreground transition-colors hover:text-destructive"
            >
              Forget this node
            </button>
          {/if}
        </div>
      {:else}
        <DialogHeader>
          <DialogTitle>Session node</DialogTitle>
          <DialogDescription>
            The machine that runs your sessions. Connect it to your Tailscale network so you can
            reach it, and the sessions it runs, from your other devices.
          </DialogDescription>
        </DialogHeader>
        {#if connect.status?.login_url}
          <a
            href={connect.status.login_url}
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex h-10 items-center justify-center gap-2 rounded-md bg-primary px-5 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
          >
            Open Tailscale sign-in <ExternalLink class="size-4" />
          </a>
          <p class="flex items-center gap-2 text-xs text-muted-foreground">
            <Loader2 class="size-3.5 animate-spin" /> Waiting for you to finish signing in…
          </p>
        {:else if sessionConnecting}
          <div class="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 class="size-4 animate-spin" /> Connecting…
          </div>
        {:else}
          <Button onclick={connectSession}>Sign in with Tailscale</Button>
        {/if}
      {/if}
    {:else if openDevice === 'web'}
      {#if webConnected}
        <DialogHeader>
          <DialogTitle class="font-mono text-xl">{webInfo?.name ?? wasmHostname()}</DialogTitle>
          <DialogDescription>Your browser, as its own device on your tailnet.</DialogDescription>
        </DialogHeader>
        <dl class="space-y-3 text-sm">
          <div class="flex justify-between gap-4">
            <dt class="text-muted-foreground">Type</dt>
            <dd>Web node</dd>
          </div>
          <div class="flex justify-between gap-4">
            <dt class="text-muted-foreground">Status</dt>
            <dd class="text-green-500">Connected</dd>
          </div>
          {#if webInfo?.tailnet}
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Tailnet</dt>
              <dd class="truncate font-mono text-xs">{webInfo.tailnet}</dd>
            </div>
          {/if}
          {#if webInfo?.dnsName}
            <div class="flex justify-between gap-4">
              <dt class="shrink-0 text-muted-foreground">MagicDNS name</dt>
              <dd class="truncate font-mono text-xs">{webInfo.dnsName}</dd>
            </div>
          {/if}
          {#if webInfo?.ipv4}
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">IPv4</dt>
              <dd class="font-mono text-xs">{webInfo.ipv4}</dd>
            </div>
          {/if}
          {#if webInfo?.ipv6}
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">IPv6</dt>
              <dd class="truncate font-mono text-xs">{webInfo.ipv6}</dd>
            </div>
          {/if}
        </dl>
        <p class="text-sm text-muted-foreground">
          This browser is its own Tailscale device, active while this tab is open. Clearing its
          saved credentials makes the next session sign in fresh; this tab stays connected until you
          close it.
        </p>
        <div class="pt-1">
          {#if webCleared}
            <p class="flex items-center gap-1.5 text-sm text-green-500">
              <Check class="size-4" /> Saved credentials cleared. Signs in fresh next session.
            </p>
          {:else}
            <Button size="sm" variant="outline" onclick={() => (clearOpen = true)}>
              Clear saved credentials
            </Button>
          {/if}
        </div>
      {:else}
        <DialogHeader>
          <DialogTitle>Web node</DialogTitle>
          <DialogDescription>
            Your browser, as its own device on your tailnet. Sign in so this browser can reach
            sessions running on your other machines.
          </DialogDescription>
        </DialogHeader>
        {#if auth.phase === 'needs_login'}
          <a
            href={auth.url}
            target="_blank"
            rel="noopener noreferrer"
            class="inline-flex h-10 items-center justify-center gap-2 rounded-md bg-primary px-5 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90"
          >
            Open Tailscale sign-in <ExternalLink class="size-4" />
          </a>
          <p class="flex items-center gap-2 text-xs text-muted-foreground">
            <Loader2 class="size-3.5 animate-spin" /> Waiting for you to finish signing in…
          </p>
        {:else if auth.phase === 'connecting'}
          <div class="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 class="size-4 animate-spin" /> Connecting…
          </div>
        {:else if auth.phase === 'error'}
          <p class="text-sm text-destructive">{auth.message}</p>
          <Button onclick={signInWeb}>Try again</Button>
        {:else}
          <Button onclick={signInWeb}>Sign in with Tailscale</Button>
        {/if}
      {/if}
    {/if}
  </DialogContent>
</Dialog>

<!-- Forget the session node: logs it out of the tailnet and wipes state. State-driven so the info
     dialog stays open behind it; confirming closes both. -->
<AlertDialog bind:open={forgetOpen}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>Forget this node?</AlertDialogTitle>
      <AlertDialogDescription>
        It is removed from your tailnet, and you sign in again to reconnect.
      </AlertDialogDescription>
    </AlertDialogHeader>
    {#if actionBusy}
      <p class="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 class="size-4 animate-spin" /> Forgetting node…
      </p>
    {/if}
    <AlertDialogFooter>
      <AlertDialogCancel disabled={actionBusy}>Cancel</AlertDialogCancel>
      <!-- A controlled Button (not AlertDialogAction, which auto-closes) so the dialog stays open and
           shows the busy state while the async forget runs; forgetSession closes it when done. -->
      <Button variant="destructive" onclick={forgetSession} disabled={actionBusy}>Forget</Button>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>

<!-- Clear the web node's saved credentials (no reload, so the current tab stays connected). -->
<AlertDialog bind:open={clearOpen}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>Clear saved credentials?</AlertDialogTitle>
      <AlertDialogDescription>
        This browser will sign in fresh next time. This tab stays connected until you close it.
      </AlertDialogDescription>
    </AlertDialogHeader>
    <AlertDialogFooter>
      <AlertDialogCancel>Cancel</AlertDialogCancel>
      <AlertDialogAction onclick={clearWebSignIn}>Clear credentials</AlertDialogAction>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>
