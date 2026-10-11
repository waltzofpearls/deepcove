<script lang="ts">
  // Asks on the session that logged in to another account, since that is where the user ran /login.
  import { untrack } from 'svelte';
  import { api } from '$lib/api/client';
  import { preferences } from '$lib/stores/preferences.svelte';
  import { usePolling } from '$lib/polling';
  import OtherAccountChoice from './OtherAccountChoice.svelte';
  import type { Session, SessionHost, SharedLoginStatus } from '$lib/api/types';
  import UserRound from '@lucide/svelte/icons/user-round';

  let { session, host }: { session: Session; host?: SessionHost } = $props();

  let status = $state<SharedLoginStatus | null>(null);

  const agent = $derived(session.agent);
  const sandboxId = $derived(session.sandbox_id ?? '');
  const running = $derived(session.status === 'running');
  const hostKey = $derived(host?.dns_name ?? '');

  $effect(() => {
    status = null;
    if (!running || !sandboxId) return;
    const currentAgent = agent;
    // Tracked through hostKey, so a fresh host object on each list poll doesn't restart the polling.
    hostKey;
    const currentHost = untrack(() => host);
    const ac = new AbortController();
    const stop = usePolling(
      async () => {
        try {
          status = await api.sharedLogin(currentAgent, currentHost, ac.signal);
        } catch {
          // An agent that cannot share, or a node that is briefly unreachable: nothing to ask.
          status = null;
        }
      },
      () => preferences.pollIntervalMs,
    );
    return () => {
      ac.abort();
      stop();
    };
  });

  const waitingOnThisSession = $derived(status?.waiting?.sandbox_id === sandboxId ? status : null);
  // Plain text, because prettier splits a period that follows a tag into "name ." in the page.
  const message = $derived(
    `This session logged in as ${waitingOnThisSession?.waiting?.account || 'another account'}. ` +
      `Your other sessions use ${waitingOnThisSession?.account || 'another one'}.`,
  );
</script>

{#if waitingOnThisSession}
  <div
    class="flex flex-wrap items-center justify-between gap-2 border-b border-border bg-primary/5 px-4 py-2 text-sm"
    role="alert"
  >
    <div class="flex min-w-0 items-center gap-2">
      <UserRound class="size-4 shrink-0 text-muted-foreground" />
      <span>{message}</span>
    </div>
    <OtherAccountChoice
      {agent}
      {host}
      status={waitingOnThisSession}
      onanswered={(next) => (status = next)}
    />
  </div>
{/if}
