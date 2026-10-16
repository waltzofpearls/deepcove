<script lang="ts">
  // One session: what it is, and what you can change about it. Each change saves with its own button.
  import { Dialog, DialogContent, DialogHeader, DialogTitle } from '$lib/components/ui/dialog';
  import { untrack } from 'svelte';
  import { Button } from '$lib/components/ui/button';
  import { api, preflightTimeoutMs } from '$lib/api/client';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { bootsVMs, runtimeLabel, RUNTIME_SANDBOXLESS } from '$lib/util/runtime';
  import { shortId } from '$lib/format';
  import SharedFolderEditor from './SharedFolderEditor.svelte';
  import WorkspaceVMDialog from './WorkspaceVMDialog.svelte';
  import VMSizePicker from './VMSizePicker.svelte';
  import LoginModePicker from './LoginModePicker.svelte';
  import { changesAgentEnv, loginChoices } from '$lib/util/loginMode';
  import { describeVMSize, formatRunningSize, sessionVMSizeCards } from '$lib/util/vmSize';
  import { folderAwaitingRestart, sizeAwaitingRestart } from '$lib/util/pendingRestart';
  import type {
    Agent,
    LoginMode,
    RuntimesInfo,
    SandboxView,
    Session,
    SessionHost,
    SharedLoginStatus,
    WorkspaceVM,
  } from '$lib/api/types';

  let {
    open = $bindable(false),
    session = $bindable<Session>(),
    host,
    onsaved,
  }: {
    open?: boolean;
    session: Session;
    host?: SessionHost;
    // Called after a save that changes the machine: a resize or a folder change.
    onsaved?: () => void;
  } = $props();

  let machineOpen = $state(false);

  // The node's name, not the host object: the list poll hands back a new object every few seconds, and
  // an effect keyed on it would fetch again each time.
  const hostKey = $derived(host?.dns_name ?? '');

  // The machine's folder and size live on the workspace record, not on the session.
  let workspaceVM = $state<WorkspaceVM | null>(null);
  $effect(() => {
    if (!open || !session.on_workspace_vm) return;
    void hostKey;
    const ac = new AbortController();
    api
      .workspaceVM(
        untrack(() => host),
        ac.signal,
      )
      .then((vm) => (workspaceVM = vm))
      .catch(() => {});
    return () => ac.abort();
  });

  const sessionId = $derived(session.id);

  // The same question the new session dialog asks, so it names the proxy here too.
  let llmProxyUrl = $state('');
  // The login picker waits for this, or it would show without the proxy until the answer came.
  let llmProxyChecked = $state(false);
  $effect(() => {
    // Cleared first: the proxy belongs to the node, and a node that does not answer has none to offer.
    llmProxyUrl = '';
    llmProxyChecked = false;
    if (!open) return;
    void hostKey;
    const ac = new AbortController();
    api
      .llmProxy(
        untrack(() => host),
        ac.signal,
      )
      .then((p) => {
        if (!ac.signal.aborted) llmProxyUrl = p.url ?? '';
      })
      .catch(() => {})
      .finally(() => {
        if (!ac.signal.aborted) llmProxyChecked = true;
      });
    return () => ac.abort();
  });

  // No guest, so there is nothing to mount into and no shared folder to edit.
  const onThisMachine = $derived(session.runtime === RUNTIME_SANDBOXLESS);
  // The picked mode, saved by its button. Keyed on the id: the dialog outlives each use.
  let loginMode = $state<LoginMode>('in_session');
  let savingLogin = $state(false);
  let loginError = $state<string | null>(null);
  $effect(() => {
    if (!open) return;
    sessionId;
    untrack(() => {
      loginMode = session.login_mode;
      loginError = null;
    });
  });
  // What this node offers: its saved API key, and what the agent allows. Per node, like the proxy.
  let loginSetup = $state<SharedLoginStatus | null>(null);
  let agent = $state<Agent | null>(null);
  $effect(() => {
    loginSetup = null;
    agent = null;
    if (!open) return;
    void hostKey;
    const currentAgent = session.agent;
    const ac = new AbortController();
    api
      .sharedLogin(
        currentAgent,
        untrack(() => host),
        ac.signal,
      )
      .then((status) => {
        if (!ac.signal.aborted) loginSetup = status;
      })
      .catch(() => {});
    api
      .listAgents()
      .then((agents) => {
        // A dialog reused for another session has its own answer coming.
        if (!ac.signal.aborted) agent = agents.find((a) => a.name === currentAgent) ?? null;
      })
      .catch(() => {});
    return () => ac.abort();
  });
  const loginChoicesHere = $derived(
    loginSetup && llmProxyChecked && agent
      ? loginChoices(
          agent,
          session.on_workspace_vm ? 'workspace_vm' : onThisMachine ? 'no_sandbox' : 'session_vm',
          {
            proxyUrl: llmProxyUrl,
            hasApiKey: loginSetup.has_api_key,
            apiKeyEnding: loginSetup.api_key_ending,
          },
        )
      : [],
  );

  async function saveLogin() {
    savingLogin = true;
    loginError = null;
    try {
      session = await api.setSessionLoginMode(session.id, loginMode, host);
      loginMode = session.login_mode;
      void sessions.refresh();
    } catch (e) {
      loginError = e instanceof Error ? e.message : String(e);
    } finally {
      savingLogin = false;
    }
  }

  // What this session runs on. Session VM and Workspace VM are VM words, so a runtime without one says
  // its own name, and a session with no sandbox runs on the machine itself.
  const machine = $derived.by(() => {
    if (onThisMachine) {
      return 'This machine';
    }
    if (!bootsVMs(session.runtime)) {
      return runtimeLabel(session.runtime);
    }
    const kind = session.on_workspace_vm ? 'Workspace VM' : 'Session VM';
    return `${kind} · ${runtimeLabel(session.runtime)}`;
  });
  // The sizes the session's own node offers. They scale with that machine's memory, and "fits N"
  // counts that machine, not this one.
  let nodeRuntimes = $state<RuntimesInfo | null>(null);
  $effect(() => {
    if (!open) return;
    void hostKey;
    nodeRuntimes = null;
    // The WASM transport ignores the abort, so a late answer for another node is dropped here instead.
    let cancelled = false;
    const ac = new AbortController();
    api
      .preflight(
        untrack(() => host),
        ac.signal,
        preflightTimeoutMs,
      )
      .then((p) => {
        if (!cancelled) nodeRuntimes = p.runtimes;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      ac.abort();
    };
  });
  const sizeCards = $derived(sessionVMSizeCards(nodeRuntimes?.session_vm_sizes ?? []));

  const size = $derived(
    session.on_workspace_vm
      ? `${describeVMSize(workspaceVM?.resources, nodeRuntimes?.workspace_vm_sizes) || 'runtime defaults'}, shared by every session on this VM`
      : describeVMSize(session.resources, nodeRuntimes?.session_vm_sizes) || 'runtime defaults',
  );

  // Only a session's own VM can change size. The workspace VM is sized from its own dialog.
  const resizable = $derived(
    !session.on_workspace_vm && !onThisMachine && bootsVMs(session.runtime),
  );

  let pickedSize = $state('');
  // Which size save is running, so only its own button says so.
  let sizeSave = $state<'restart now' | 'restart later' | null>(null);
  const resizing = $derived(sizeSave !== null);
  let resizeError = $state<string | null>(null);
  $effect(() => {
    if (!open) return;
    sessionId;
    untrack(() => {
      pickedSize = session.resources?.size ?? '';
      resizeError = null;
    });
  });

  const sizeChanged = $derived(Boolean(pickedSize) && pickedSize !== session.resources?.size);
  // Starting counts as running: a restart passes through it, and the buttons must not change under you.
  const running = $derived(session.status === 'running' || session.status === 'starting');
  const loginChanged = $derived(loginMode !== session.login_mode);
  // Only a running agent restarts, and only for a change it reads at start.
  const loginSaveRestarts = $derived(
    loginChanged && running && changesAgentEnv(session.login_mode, loginMode),
  );

  async function saveAndRestart() {
    await applySize('restart now', () => api.resizeSessionVM(session.id, pickedSize, host));
  }

  async function saveForLater() {
    await applySize('restart later', () => api.saveSessionVMSize(session.id, pickedSize, host));
  }

  async function applySize(
    which: 'restart now' | 'restart later',
    request: () => Promise<Session>,
  ) {
    sizeSave = which;
    resizeError = null;
    try {
      session = await request();
      void sessions.refresh();
      onsaved?.();
      await loadRunningSandbox();
    } catch (e) {
      resizeError = e instanceof Error ? e.message : String(e);
    } finally {
      sizeSave = null;
    }
  }

  // The VM this session runs on, its own or the workspace VM, as it runs right now. Used to tell saved
  // changes it has not picked up yet.
  let runningSandbox = $state<SandboxView | null>(null);

  async function loadRunningSandbox() {
    const sandboxID = session.sandbox_id;
    if (onThisMachine || !sandboxID) {
      runningSandbox = null;
      return;
    }
    try {
      const views = await api.runtimes(host);
      runningSandbox =
        views.runtimes
          .flatMap((view) => view.sandboxes)
          .find((sandbox) => sandbox.sandbox_id === sandboxID && sandbox.state === 'running') ??
        null;
    } catch {
      runningSandbox = null;
    }
  }

  $effect(() => {
    if (!open) return;
    void session.sandbox_id;
    untrack(() => void loadRunningSandbox());
  });

  const savedSizeWaiting = $derived(
    resizable &&
      running &&
      // Mid save, the new size is in but the running one has not been read again yet.
      !resizing &&
      sizeAwaitingRestart(
        runningSandbox?.resources,
        session.resources,
        nodeRuntimes?.session_vm_sizes,
      ),
  );

  const workspaceChangesWaiting = $derived(
    session.on_workspace_vm &&
      runningSandbox !== null &&
      workspaceVM !== null &&
      (sizeAwaitingRestart(
        runningSandbox.resources,
        workspaceVM.resources,
        nodeRuntimes?.workspace_vm_sizes,
      ) ||
        folderAwaitingRestart(
          runningSandbox.mounts,
          workspaceVM.host_root,
          workspaceVM.guest_root,
        )),
  );
  const sessionsOnWorkspaceVM = $derived(runningSandbox?.sessions.length ?? 0);

  let restartingVM = $state(false);

  async function restartVM() {
    if (!session.sandbox_id) return;
    restartingVM = true;
    resizeError = null;
    try {
      await api.restartSandbox(session.runtime, session.sandbox_id, host);
      void sessions.refresh();
      await loadRunningSandbox();
      onsaved?.();
    } catch (e) {
      resizeError = e instanceof Error ? e.message : String(e);
    } finally {
      restartingVM = false;
    }
  }
</script>

<Dialog bind:open>
  <DialogContent class="max-h-[90vh] w-[calc(100%-2rem)] overflow-y-auto sm:max-w-2xl">
    <DialogHeader>
      <DialogTitle class="font-mono">{session.name}</DialogTitle>
    </DialogHeader>

    <dl class="grid grid-cols-[7rem_1fr] gap-x-4 gap-y-2 text-sm">
      <dt class="text-muted-foreground">Agent</dt>
      <dd>{session.agent}</dd>

      <dt class="text-muted-foreground">Status</dt>
      <dd>{session.status}</dd>

      <dt class="text-muted-foreground">Machine</dt>
      <dd>{machine}</dd>

      <!-- Nothing caps a session that runs on the machine itself, so there is no size to show. -->
      {#if !onThisMachine}
        <dt class="text-muted-foreground">Size</dt>
        <dd>{size}</dd>
      {/if}

      <dt class="text-muted-foreground">Folder</dt>
      <dd class="font-mono text-xs break-all">
        {#if session.work_dir}
          {session.work_dir}
          {#if workspaceVM}
            <span class="text-muted-foreground">(in {workspaceVM.host_root})</span>
          {/if}
        {:else if onThisMachine}
          {session.agent_work_dir || 'a folder deepcove made for this session'}
        {:else if session.mounts?.length}
          {session.mounts.map((m) => `${m.host_path} → ${m.container_path}`).join(', ')}
        {:else}
          /workspace <span class="text-muted-foreground">(nothing mounted)</span>
        {/if}
      </dd>

      {#if session.image}
        <dt class="text-muted-foreground">Image</dt>
        <dd class="font-mono text-xs break-all">{session.image}</dd>
      {/if}

      <dt class="text-muted-foreground">Sandbox</dt>
      <dd class="font-mono text-xs">{shortId(session.sandbox_id ?? '', 24) || 'none yet'}</dd>

      <dt class="text-muted-foreground">Session id</dt>
      <dd class="font-mono text-xs break-all">{session.id}</dd>
    </dl>

    {#if loginChoicesHere.length > 1}
      <div class="space-y-2 border-t border-border pt-4">
        <LoginModePicker
          choices={loginChoicesHere}
          value={loginMode}
          disabled={savingLogin}
          onpick={(mode) => (loginMode = mode)}
        />
        {#if loginSaveRestarts}
          <p class="text-xs text-muted-foreground">
            Saving restarts the agent, so it picks up the new login.
          </p>
        {/if}
        {#if loginError}<p class="text-xs text-destructive">{loginError}</p>{/if}
        <div class="flex items-center justify-end">
          <Button size="sm" disabled={savingLogin || !loginChanged} onclick={saveLogin}>
            {#if loginSaveRestarts}
              {savingLogin ? 'Restarting agent…' : 'Save and restart agent'}
            {:else}
              {savingLogin ? 'Saving…' : 'Save login'}
            {/if}
          </Button>
        </div>
      </div>
    {/if}

    {#if resizable && sizeCards.length}
      <div class="space-y-3 border-t border-border pt-4">
        <VMSizePicker
          title="Session VM size"
          cards={sizeCards}
          selected={pickedSize}
          onselect={(name) => (pickedSize = name)}
        />
        {#if savedSizeWaiting && runningSandbox}
          <div
            class="flex items-start justify-between gap-4 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-sm"
            role="status"
          >
            <div class="space-y-1">
              <div class="font-medium">The new size is saved but not in use yet.</div>
              <p class="text-xs text-muted-foreground">
                The VM still runs with {formatRunningSize(runningSandbox.resources)}. It switches to
                {describeVMSize(session.resources, nodeRuntimes?.session_vm_sizes)} when it restarts.
                Restarting interrupts this session.
              </p>
            </div>
            <Button
              size="sm"
              class="shrink-0"
              disabled={restartingVM || resizing}
              onclick={restartVM}
            >
              {restartingVM ? 'Restarting…' : 'Restart now'}
            </Button>
          </div>
        {/if}
        {#if !savedSizeWaiting || sizeChanged}
          <p class="text-xs text-muted-foreground">
            {#if running}
              A new size takes effect when the VM restarts. Restarting interrupts this session. Your
              home folder and login stay.
            {:else}
              The VM gets this size when it next starts.
            {/if}
          </p>
        {/if}
        <div class="flex items-center justify-end gap-2">
          {#if running}
            <Button
              size="sm"
              variant="secondary"
              disabled={resizing || restartingVM || !sizeChanged}
              onclick={saveForLater}
            >
              {sizeSave === 'restart later' ? 'Saving…' : 'Save, restart later'}
            </Button>
            <Button
              size="sm"
              disabled={resizing || restartingVM || !sizeChanged}
              onclick={saveAndRestart}
            >
              {sizeSave === 'restart now' ? 'Restarting…' : 'Save and restart'}
            </Button>
          {:else}
            <Button size="sm" disabled={resizing || !sizeChanged} onclick={saveForLater}>
              Save size
            </Button>
          {/if}
        </div>
        {#if resizeError}<p class="text-xs text-destructive">{resizeError}</p>{/if}
      </div>
    {/if}

    <div class="border-t border-border pt-4">
      {#if session.on_workspace_vm}
        <div class="flex items-start justify-between gap-4">
          <div class="space-y-1">
            <p class="text-sm font-medium">This is the workspace VM</p>
            <p class="text-xs leading-relaxed text-muted-foreground">
              Its folder belongs to the machine, so changing it changes every session on it.
            </p>
          </div>
          <!-- Disabled until the record arrives: the dialog with no record is the CREATE form, and
               saving that would overwrite this machine's folder and size with blanks. -->
          <Button
            size="sm"
            variant="secondary"
            class="shrink-0"
            disabled={!workspaceVM}
            onclick={() => (machineOpen = true)}
          >
            Edit the machine
          </Button>
        </div>
        {#if workspaceChangesWaiting}
          <div
            class="mt-3 flex items-start justify-between gap-4 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-sm"
            role="status"
          >
            <div class="space-y-1">
              <div class="font-medium">The workspace VM has saved changes not in use yet.</div>
              <p class="text-xs text-muted-foreground">
                Restarting applies them and interrupts
                {sessionsOnWorkspaceVM === 1
                  ? 'the 1 session'
                  : `all ${sessionsOnWorkspaceVM} sessions`}
                on this VM.
              </p>
            </div>
            <Button
              size="sm"
              class="shrink-0"
              disabled={restartingVM || resizing}
              onclick={restartVM}
            >
              {restartingVM ? 'Restarting…' : 'Restart now'}
            </Button>
          </div>
          {#if resizeError}<p class="mt-2 text-xs text-destructive">{resizeError}</p>{/if}
        {/if}
      {:else if onThisMachine}
        <p class="text-xs leading-relaxed text-muted-foreground">
          This session runs on the machine, so there is nothing to mount: the agent reads the folder
          it works in directly.
        </p>
      {:else}
        <SharedFolderEditor bind:session {host} {onsaved} />
      {/if}
    </div>
  </DialogContent>
</Dialog>

{#if workspaceVM}
  <WorkspaceVMDialog
    bind:open={machineOpen}
    vm={workspaceVM}
    {host}
    onsaved={() => {
      void api
        .workspaceVM(host)
        .then((vm) => (workspaceVM = vm))
        .catch(() => {});
      void loadRunningSandbox();
      onsaved?.();
    }}
  />
{/if}
