<script lang="ts">
  // The two answers when a session logged in to another account. Used by the banner and the Settings card.
  import { Button } from '$lib/components/ui/button';
  import { api, ApiError } from '$lib/api/client';
  import type { SessionHost, SharedLoginStatus } from '$lib/api/types';

  let {
    agent,
    host,
    status,
    onanswered,
  }: {
    agent: string;
    host?: SessionHost;
    status: SharedLoginStatus;
    onanswered: (next: SharedLoginStatus) => void;
  } = $props();

  const sharedAccount = $derived(status.account || 'the current account');
  const waitingAccount = $derived(status.waiting?.account || 'the new account');

  let answering = $state<'keep' | 'switch' | null>(null);
  let answerError = $state('');

  async function answer(choice: 'keep' | 'switch') {
    answering = choice;
    answerError = '';
    try {
      onanswered(
        choice === 'keep'
          ? await api.keepSharedLogin(agent, host)
          : await api.switchSharedLogin(agent, host),
      );
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        // Answered somewhere else first, so show what was chosen.
        onanswered(await api.sharedLogin(agent, host).catch(() => ({ ...status, waiting: null })));
        return;
      }
      answerError = e instanceof Error ? e.message : String(e);
    } finally {
      answering = null;
    }
  }
</script>

<div class="flex flex-wrap items-center gap-2">
  <Button size="sm" variant="outline" disabled={answering !== null} onclick={() => answer('keep')}>
    {answering === 'keep' ? 'Keeping…' : `Keep ${sharedAccount}`}
  </Button>
  <Button size="sm" disabled={answering !== null} onclick={() => answer('switch')}>
    {answering === 'switch' ? 'Switching…' : `Switch all to ${waitingAccount}`}
  </Button>
  {#if answerError}
    <span class="text-xs text-destructive">{answerError}</span>
  {/if}
</div>
