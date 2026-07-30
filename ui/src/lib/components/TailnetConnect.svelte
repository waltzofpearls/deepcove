<script lang="ts">
  import { api } from '$lib/api/client';
  import type { ConnectStatus } from '$lib/api/types';
  import {
    AlertDialog,
    AlertDialogContent,
    AlertDialogHeader,
    AlertDialogTitle,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogCancel,
  } from '$lib/components/ui/alert-dialog';
  import { Button } from '$lib/components/ui/button';
  import Loader2 from '@lucide/svelte/icons/loader-2';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import ExternalLink from '@lucide/svelte/icons/external-link';
  import Globe from '@lucide/svelte/icons/globe';

  // onconnected fires once when the node finishes connecting (phase → running). Lets a host (e.g. the
  // Nodes empty state) react, flipping from the connect CTA to the node list, without polling itself.
  let { onconnected }: { onconnected?: () => void } = $props();

  let status = $state<ConnectStatus | null>(null);
  let starting = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let copied = $state(false);

  // Poll while the flow is in progress, or while running over http so the UI
  // flips to https the moment the user enables HTTPS in their admin.
  const polling = $derived(
    !!status &&
      (status.phase === 'starting' ||
        status.phase === 'needs_login' ||
        (status.phase === 'running' && !status.tls)),
  );

  const ownerName = $derived(
    status?.owner ? status.owner.display_name || status.owner.login_name || '' : '',
  );

  async function refresh() {
    try {
      status = await api.connectStatus();
      error = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  async function connect() {
    starting = true;
    error = null;
    try {
      status = await api.connect();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      starting = false;
    }
  }

  async function disconnect() {
    busy = true;
    error = null;
    try {
      status = await api.disconnect();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
    }
  }

  let forgetOpen = $state(false);
  async function forget() {
    busy = true;
    error = null;
    try {
      status = await api.resetConnect();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      busy = false;
      forgetOpen = false;
    }
  }

  async function copyUrl() {
    if (!status?.url) return;
    try {
      await navigator.clipboard.writeText(status.url);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      /* clipboard needs a secure context; the link is still selectable */
    }
  }

  $effect(() => {
    refresh();
  });
  $effect(() => {
    if (!polling) return;
    const t = setInterval(refresh, 2500);
    return () => clearInterval(t);
  });

  let firedConnected = false;
  $effect(() => {
    if (status?.phase === 'running' && !firedConnected) {
      firedConnected = true;
      onconnected?.();
    }
  });
</script>

<section class="space-y-3">
  <h2 class="text-sm font-medium text-muted-foreground">Access from your other devices</h2>

  {#if !status || status.phase === 'stopped'}
    <p class="text-xs text-muted-foreground">
      Connect this machine to your tailnet to reach deepcove from your phone, work laptop, or other
      devices, over Tailscale, identified as you.
    </p>
    <Button size="sm" onclick={connect} disabled={starting}>
      {#if starting}<Loader2 class="size-4 animate-spin" />{/if}
      Connect to your tailnet
    </Button>
  {:else if status.phase === 'starting' || status.phase === 'needs_login'}
    <div class="space-y-2 rounded-md border border-border p-3">
      <div class="flex items-center gap-2 text-sm">
        <Loader2 class="size-4 animate-spin text-muted-foreground" />
        {status.phase === 'needs_login' ? 'Waiting for you to sign in…' : 'Starting…'}
      </div>
      {#if status.login_url}
        <a
          href={status.login_url}
          target="_blank"
          rel="noopener"
          class="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:underline"
        >
          <ExternalLink class="size-4" /> Open Tailscale sign-in
        </a>
        <p class="break-all font-mono text-[11px] text-muted-foreground">{status.login_url}</p>
      {/if}
    </div>
  {:else if status.phase === 'running'}
    <div class="space-y-2 rounded-md border border-border p-3">
      <div class="flex items-center gap-2 text-sm font-medium text-[var(--color-success)]">
        <Globe class="size-4" /> Connected{ownerName ? ` as ${ownerName}` : ''}
      </div>
      <p class="text-xs text-muted-foreground">
        Reachable at this address from any device signed in to{status.tailnet_name
          ? ` the ${status.tailnet_name}`
          : ' this'} tailnet. The device you open it on must have Tailscale connected here.
      </p>
      <div
        class="flex items-center gap-2 rounded-md border border-border bg-secondary p-2 font-mono text-xs"
      >
        <a href={status.url} target="_blank" rel="noopener" class="flex-1 truncate hover:underline"
          >{status.url}</a
        >
        <Button
          size="icon"
          variant="ghost"
          class="size-7 shrink-0"
          onclick={copyUrl}
          aria-label="Copy URL"
        >
          {#if copied}
            <Check class="size-3.5 text-[var(--color-success)]" />
          {:else}
            <Copy class="size-3.5" />
          {/if}
        </Button>
      </div>
      {#if !status.https_enabled}
        <p class="text-[11px] text-muted-foreground">
          Serving over http. Enable <strong>MagicDNS</strong> + <strong>HTTPS certificates</strong>
          in your
          <a
            href="https://login.tailscale.com/admin/dns"
            target="_blank"
            rel="noopener"
            class="text-primary hover:underline">Tailscale admin</a
          >
          to upgrade to https automatically, no restart needed.
        </p>
      {/if}
      <div class="flex items-center gap-3 pt-1">
        <Button size="sm" variant="outline" onclick={disconnect} disabled={busy}>Disconnect</Button>
        <button
          type="button"
          onclick={() => (forgetOpen = true)}
          disabled={busy}
          class="text-xs text-muted-foreground transition-colors hover:text-destructive disabled:opacity-50"
        >
          Forget this node
        </button>
      </div>
    </div>
  {/if}

  {#if error}
    <p class="text-xs text-destructive">{error}</p>
  {/if}
</section>

<AlertDialog bind:open={forgetOpen}>
  <AlertDialogContent>
    <AlertDialogHeader>
      <AlertDialogTitle>Forget this node?</AlertDialogTitle>
      <AlertDialogDescription>
        It is removed from your tailnet, and you sign in again to reconnect.
      </AlertDialogDescription>
    </AlertDialogHeader>
    {#if busy}
      <p class="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 class="size-4 animate-spin" /> Forgetting node…
      </p>
    {/if}
    <AlertDialogFooter>
      <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
      <!-- Controlled Button (not AlertDialogAction, which auto-closes) so the dialog stays open and
           shows the busy state while the async forget runs; forget() closes it when done. -->
      <Button variant="destructive" onclick={forget} disabled={busy}>Forget</Button>
    </AlertDialogFooter>
  </AlertDialogContent>
</AlertDialog>
