<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { opensInNewTab } from '$lib/util/links';
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogHeader,
    DialogTitle,
  } from '$lib/components/ui/dialog';
  import { Button } from '$lib/components/ui/button';
  import { Input } from '$lib/components/ui/input';
  import { Label } from '$lib/components/ui/label';
  import { Switch } from '$lib/components/ui/switch';
  import { api, preflightTimeoutMs } from '$lib/api/client';
  import { discoveryRequired } from '$lib/api/transport';
  import { discovery } from '$lib/stores/discovery.svelte';
  import { friendlyError, type FriendlyError } from '$lib/api/errors';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { onlineHostNodes, defaultNodeId, nodeHost } from '$lib/api/sessions';
  import HostPathField from './HostPathField.svelte';
  import WorkspaceVMFields from './WorkspaceVMFields.svelte';
  import VMSizePicker from './VMSizePicker.svelte';
  import { shellQuote } from '$lib/util/shell';
  import { pathInsideFolder } from '$lib/util/path';
  import type {
    Agent,
    MountRequest,
    NodeInfo,
    RuntimesInfo,
    SessionResources,
    WorkspaceVM,
  } from '$lib/api/types';
  import {
    bootsVMs,
    runtimeHint,
    runtimeLabel,
    missingReasons,
    runtimeSetupOffered,
    showsRuntimePicker,
    RUNTIME_SANDBOXLESS,
  } from '$lib/util/runtime';
  import { DEFAULT_VM_SIZE, sessionVMSizeCards } from '$lib/util/vmSize';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import Plus from '@lucide/svelte/icons/plus';
  import X from '@lucide/svelte/icons/x';
  import Download from '@lucide/svelte/icons/download';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';

  let { open = $bindable(false) }: { open?: boolean } = $props();

  type FormMount = { host_path: string; container_path: string; read_only: boolean };
  type ImageStrategy = 'install' | 'official' | 'custom';

  let agents = $state<Agent[]>([]);
  let agentsError = $state<string | null>(null);
  let agent = $state('claude');
  let name = $state('');

  // Compute selection (multinode): which of your machines hosts the session. The selector only
  // shows when you have more than one online node; otherwise the session is created here.
  let nodes = $state<NodeInfo[]>([]);
  let nodeId = $state('');
  const hostNodes = $derived(onlineHostNodes(nodes));

  // The only machine that can host, when it is not the one serving this page. That is the web node
  // case, where "run on this machine" would be wrong and nothing else says where the session goes.
  const soleRemoteNode = $derived(
    hostNodes.length === 1 && !hostNodes[0].self ? hostNodes[0] : null,
  );
  // The REMOTE node a new session will be created on, used to scope create-time calls (filesystem
  // browsing, preflight, image checks) to that machine over WASM. Undefined when the target is this
  // node. Self is reached directly via the boot transport, NOT over WASM (routing self over WASM would
  // dial a tailnet node that isn't signed in here and hang). Also undefined with a single node.
  const selectedHost = $derived.by(() => {
    if (hostNodes.length <= 1) return undefined;
    const node = hostNodes.find((n) => n.stable_node_id === nodeId);
    if (!node || node.self) return undefined;
    return nodeHost(node);
  });
  // A stable key for that host. selectedHost is a fresh object on every recompute, so an effect that
  // depends on it re-runs forever; the dns name changes only when the choice does.
  const selectedHostKey = $derived(selectedHost?.dns_name ?? '');
  // The last auto-suggested name, so the prefill effect can refresh it on agent
  // change without clobbering a name the user has typed.
  let suggestedName = $state('');
  // Prefer-setting: start with one folder row ready to fill (the conventional
  // /workspace). The user can opt out ("Run without a folder") to clear it.
  let mounts = $state<FormMount[]>([
    { host_path: '', container_path: '/workspace', read_only: false },
  ]);
  // The deepcove host's home dir, where the host-path picker/autocomplete starts.
  // Fetched on open so we can pre-fill the host-path field (with a trailing slash
  // so suggestions appear immediately) instead of leaving it blank.
  let homeDir = $state('');

  // Each strategy keeps its own fields so switching radios doesn't lose what's typed.
  let imageStrategy = $state<ImageStrategy>('install');
  let baseImage = $state('');
  let setupScript = $state('');
  let customImage = $state('');
  let customSetup = $state('');

  let submitting = $state(false);
  let submitError = $state<FriendlyError | null>(null);
  // Image readiness: null = not yet checked, false = absent (will be
  // downloaded on create), true = already present. Checked proactively
  // as the agent/image selection changes.
  let imagePresent = $state<boolean | null>(null);
  // Download state while pulling: percent is -1 until the registry reports
  // layer sizes (rendered as an indeterminate bar).
  let pulling = $state(false);
  let pullPercent = $state(-1);
  let pullStatus = $state('');
  let copied = $state(false);

  $effect(() => {
    if (!open || agents.length > 0) return;
    api
      .listAgents()
      .then((a) => {
        agents = a;
        if (a.length > 0 && !a.find((x) => x.name === agent)) {
          agent = a[0].name;
        }
      })
      .catch((e) => (agentsError = String(e)));
  });

  // Load the user's nodes on open and default the target to this machine. Connected mode reads them
  // from the server; dashboard mode reads the in-browser node's discovery. Best-effort: a failure just
  // hides the selector and creates here.
  $effect(() => {
    if (!open || nodes.length > 0) return;
    if (discoveryRequired()) {
      nodes = discovery.nodes;
      nodeId = defaultNodeId(discovery.nodes);
      return;
    }
    api
      .nodes()
      .then((list) => {
        nodes = list;
        nodeId = defaultNodeId(list);
      })
      .catch(() => {});
  });

  // Which runtimes the create target offers, and which of them can host right now. Both are properties
  // of the *selected* node (a Linux node will never offer Apple VF), so they are fetched from that node
  // rather than assumed from the node serving this page. Otherwise we would block you on the primary's
  // state when the target is fine. Null until fetched, in which case we fall back to the global store.
  let nodeRuntimes = $state<RuntimesInfo | null>(null);
  // True while the picked node has not answered yet.
  let waitingForNodeRuntimes = $state(false);
  // Set when the picked node never answered. Its runtimes are then unknown, and this node's are not a
  // stand-in: offering them would send a runtime that node may not have.
  let nodeRuntimesError = $state<string | null>(null);
  // Bumped by Retry. A cold tailnet dial can outlast the timeout once and answer the next time.
  let nodeRuntimesAttempt = $state(0);
  $effect(() => {
    const key = selectedHostKey;
    void nodeRuntimesAttempt;
    const host = untrack(() => selectedHost);
    nodeRuntimes = null;
    nodeRuntimesError = null;
    // A pick belongs to the node it was made for, and to this visit: reopening or switching nodes
    // starts from that node's own default, so No sandbox is never carried over unseen.
    runtimeChoice = '';
    waitingForNodeRuntimes = Boolean(open && key);
    if (!open || !key) return;
    let cancelled = false;
    const ac = new AbortController();
    api
      .preflight(host, ac.signal, preflightTimeoutMs)
      .then((p) => {
        if (cancelled) return;
        nodeRuntimes = p.runtimes;
      })
      .catch(() => {
        if (cancelled) return;
        nodeRuntimesError = `Couldn't reach ${host?.hostname ?? 'that machine'} to see what it can run.`;
      })
      .finally(() => {
        if (!cancelled) waitingForNodeRuntimes = false;
      });
    return () => {
      cancelled = true;
      ac.abort();
    };
  });

  const preflightLoaded = $derived(
    !waitingForNodeRuntimes && (nodeRuntimes !== null || preflight.loaded),
  );

  // The runtimes the create target offers: the selected node's when known, else this node's.
  //
  // Nothing while the picked node is still answering. This page's own runtimes are not its runtimes, so
  // showing them would offer a choice that node may not have.
  const runtimes = $derived(
    waitingForNodeRuntimes || nodeRuntimesError ? null : (nodeRuntimes ?? preflight.runtimes),
  );
  const runtimeOptions = $derived(runtimes?.available ?? []);
  // No sandbox is still offered when it can't run yet, greyed out with what it needs.
  const sandboxlessNeedsTmux = $derived(runtimes?.missing?.[RUNTIME_SANDBOXLESS] === 'no_tmux');
  const showRuntimePicker = $derived(showsRuntimePicker(runtimeOptions) || sandboxlessNeedsTmux);
  // Only the user's explicit pick is stored. Resolving it against the node happens in a $derived
  // below, NOT in an $effect: an effect that both reads and writes the same state re-triggers itself,
  // which in Svelte 5 runs until effect_update_depth_exceeded and hangs the page.
  let runtimeChoice = $state('');
  // What the create will actually use. A pick the target node does not offer falls back to that node's
  // default, which can be empty ("Choose one"), so switching nodes can never leave an unavailable runtime
  // selected. Image checks and pulls target this too, since each runtime keeps its own image store.
  const effectiveRuntime = $derived.by(() => {
    if (runtimeChoice && runtimeOptions.includes(runtimeChoice)) return runtimeChoice;
    return runtimes?.default ?? '';
  });

  // Nothing to shape the form around yet: the node is still answering or never did, has nothing to run
  // on, or is waiting for you to pick. Until then the runtime-specific sections stay hidden rather than
  // guess.
  const runtimeKnown = $derived(effectiveRuntime !== '');
  // Workspace and session VMs are VM concepts. Anywhere else there is no machine to choose.
  const onVMRuntime = $derived(bootsVMs(effectiveRuntime));
  // This machine offers no VM yet but deepcove can set one up. Without this line the picker offers only
  // No sandbox, and nothing says that isolation is one download away.
  const vmSetupOffered = $derived(
    !(selectedHost ?? soleRemoteNode) &&
      runtimeSetupOffered(runtimes) &&
      !runtimeOptions.some(bootsVMs),
  );
  // A plain click goes to the page, so the dialog closes. A cmd or ctrl click opens a new tab and keeps the form.
  function closeUnlessNewTab(e: MouseEvent) {
    if (opensInNewTab(e)) return;
    open = false;
  }
  // The same, for a remote node: its own page under Nodes has the Set up button.
  const remoteVMSetupOffered = $derived(
    runtimeSetupOffered(runtimes) && !runtimeOptions.some(bootsVMs)
      ? (selectedHost ?? soleRemoteNode)
      : null,
  );

  // No sandbox means no image and no size caps, and the folder is a real folder on that machine rather
  // than something mounted into a guest.
  const onSandboxless = $derived(effectiveRuntime === RUNTIME_SANDBOXLESS);
  // The agent runs as the user deepcove runs as, so it reads that user's own login.
  const loginNote = $derived(
    onSandboxless ? 'the login already on this machine' : "this session's own login",
  );
  // Where that runtime puts the folder it makes, so the form can show it before the session exists.
  const sandboxlessRoot = $derived(runtimes?.workspace_roots?.[effectiveRuntime] ?? '');
  // Off means deepcove makes the folder and owns it. On means you pick one, and it is never deleted.
  let ownFolder = $state(false);

  // Agents this machine does not have yet, so the dropdown can say that picking one installs it.
  const agentsToInstall = $derived(runtimes?.agents_to_install?.[effectiveRuntime] ?? []);
  // The option text never changes, so the list cannot rewrite itself when the node answers. What the
  // node said goes in a line under it, once it has said anything.
  const installsTheAgent = $derived(runtimes !== null && agentsToInstall.includes(agent));

  // Why the SELECTED runtime cannot host a session, or null when it can. Asked per runtime, because one
  // being down says nothing about another. A node with no runtime at all has nothing to select.
  const runtimeBlocked = $derived.by(() => {
    if (nodeRuntimesError) return nodeRuntimesError;
    if (!runtimes) return null;
    if (runtimeOptions.length > 0) return runtimes.unavailable?.[effectiveRuntime] ?? null;
    const where = (selectedHost ?? soleRemoteNode)?.hostname ?? 'This machine';
    const reasons = runtimeSetupOffered(runtimes) ? [] : missingReasons(runtimes);
    return [`${where} has nothing to run sessions on yet.`, ...reasons].join(' ');
  });
  // The remote node behind that message, whose own page can set it up from here.
  const remoteNodeWithNothing = $derived(
    runtimes && runtimeOptions.length === 0 && runtimeSetupOffered(runtimes)
      ? (selectedHost ?? soleRemoteNode)
      : null,
  );
  // Back from setting the node up in another tab: ask it again. Only while nothing is picked, since a
  // fresh answer resets the pick.
  $effect(() => {
    const waitingOnSetup =
      remoteNodeWithNothing || ((vmSetupOffered || remoteVMSetupOffered) && !runtimeChoice);
    if (!open || !waitingOnSetup) return;
    // A single node is read from the shared preflight rather than asked directly.
    const recheck = () => (selectedHostKey ? nodeRuntimesAttempt++ : void preflight.refresh());
    window.addEventListener('focus', recheck);
    return () => window.removeEventListener('focus', recheck);
  });
  // Another option could host the session right now.
  const otherIsolationUsable = $derived(
    runtimeOptions.some((name) => name !== effectiveRuntime && !runtimes?.unavailable?.[name]),
  );

  // Where the session runs: this node's workspace VM, or one of its own. The workspace VM is the default
  // once a workspace VM exists, since that is the cheap answer when you already have one.
  // The workspace VM is the default answer, including when there is none yet: this dialog then
  // creates it, which is the flow most people want the first time.
  let placement = $state<'workspace' | 'private'>('workspace');
  let workspaceVM = $state<WorkspaceVM | null>(null);
  let workspaceHostRoot = $state('');
  let workspaceGuestRoot = $state('/workspace');
  let workspaceSize = $state(DEFAULT_VM_SIZE);
  let workDir = $state('');
  // The session's folder on the workspace VM, as a path on this machine inside the VM's shared folder.
  let workspaceFolder = $state('');
  $effect(() => {
    const hostRoot = workspaceVM?.host_root;
    if (hostRoot && !untrack(() => workspaceFolder)) workspaceFolder = hostRoot + '/';
  });
  // What the server is sent: a name inside the shared folder, '' for the session's own name, or null when
  // the folder is outside it.
  const workspaceFolderName = $derived(
    workspaceVM ? pathInsideFolder(workspaceVM.host_root, workspaceFolder) : '',
  );
  // Only on a runtime that boots VMs. The Machine picker is hidden otherwise, so a node without one
  // would keep the default and ask for a workspace VM it cannot build.
  const onWorkspace = $derived(placement === 'workspace' && onVMRuntime);
  // No machine yet means this dialog creates it too, so it asks for the folder and size first.
  const creatingWorkspaceVM = $derived(onWorkspace && workspaceVM === null);
  // Which controls this session shows. If a control is hidden, it cannot stop you from creating the
  // session, so the Create button checks these too.
  const showsImageControls = $derived(
    runtimeKnown && (!onWorkspace || creatingWorkspaceVM) && !onSandboxless,
  );
  const showsSizeControls = $derived(runtimeKnown && !onWorkspace && !onSandboxless);

  // Reads the workspace VM when the dialog opens. Null is normal rather than an error: this node
  // just does not have one yet.
  // The machine belongs to the node that will host the session, so this follows the node picker rather
  // than always asking this one.
  $effect(() => {
    if (!open) return;
    void selectedHostKey;
    const host = untrack(() => selectedHost);
    const ac = new AbortController();
    workspaceVM = null;
    api
      .workspaceVM(host, ac.signal)
      .then((vm) => (workspaceVM = vm))
      .catch(() => {});
    return () => ac.abort();
  });

  // The size you picked, or empty to take the node's default size.
  let pickedVMSize = $state('');
  const vmSize = $derived(pickedVMSize || runtimes?.default_vm_size || DEFAULT_VM_SIZE);
  const sessionVMSizes = $derived(sessionVMSizeCards(runtimes?.session_vm_sizes ?? []));
  const pickedSizeTooBig = $derived(
    sessionVMSizes.find((card) => card.name === vmSize)?.tooBigForThisMachine ?? false,
  );

  // Resolve the home dir of the node the session will run on, then seed the default folder row with it.
  // Follows the node picker: your Mac's home is not a path that exists on a droplet.
  $effect(() => {
    void selectedHostKey;
    const host = untrack(() => selectedHost);
    if (!open) return;
    // A folder belongs to one machine, so opening this or picking another node starts over.
    untrack(() => {
      ownFolder = false;
      workDir = '';
      workspaceFolder = '';
    });
    let cancelled = false;
    api
      .fsList(undefined, host)
      .then((res) => {
        if (cancelled) return;
        const previous = untrack(() => homeDir);
        homeDir = res.path;
        untrack(() => {
          // Never clobber a folder you typed, only a blank row or the last node's home.
          const first = mounts[0];
          if (first && (!first.host_path || first.host_path === previous + '/')) {
            first.host_path = res.path + '/';
          }
          // Blank only: a slow node must not overwrite a folder you typed while it answered.
          if (!workDir) {
            workDir = res.path + '/';
          }
        });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  });

  // Pre-fill the Name field with a generated suggestion on open and when the agent
  // changes, but never clobber a name the user has typed (only overwrite when the
  // field is empty or still shows the previous suggestion).
  $effect(() => {
    if (!open) return;
    const a = agent;
    api
      .suggestName(a)
      .then((n) => {
        if (!name || name === suggestedName) name = n;
        suggestedName = n;
      })
      .catch(() => {});
  });

  // Re-seed image-related defaults whenever the agent changes. Switching agents
  // mid-edit is rare; the registry defaults are almost always what you want.
  // The strategy default prefers the agent's prebuilt image when one exists,
  // since that's the fastest-boot path.
  const selectedAgent = $derived(agents.find((a) => a.name === agent));
  $effect(() => {
    if (!selectedAgent) return;
    baseImage = selectedAgent.sandboxed.default_image;
    setupScript = selectedAgent.sandboxed.setup;
    imageStrategy = selectedAgent.sandboxed.official_image ? 'official' : 'install';
  });

  const command = $derived.by(() => {
    const parts = ['deepcove', 'session', agent];
    const trimmedName = name.trim();
    if (trimmedName) parts.push('--name', shellQuote(trimmedName));
    // The machine settings below belong to the workspace VM, not to a session on it.
    if (onWorkspace) {
      parts.push('--workspace');
      if (workspaceFolderName) parts.push('--work-dir', shellQuote(workspaceFolderName));
      return parts.join(' ');
    }
    // Matches buildPayload, which sends a runtime only while the picker is on screen.
    if (showRuntimePicker && effectiveRuntime) parts.push('--runtime', effectiveRuntime);
    if (onSandboxless) {
      // No image, no setup script and no mounts on this runtime, so the folder is all that is left.
      if (ownFolder && workDir.trim()) parts.push('--work-dir', shellQuote(workDir.trim()));
      return parts.join(' ');
    }
    if (imageStrategy === 'official' && selectedAgent?.sandboxed.official_image) {
      parts.push('--image', shellQuote(selectedAgent.sandboxed.official_image));
    } else if (imageStrategy === 'custom' && customImage) {
      parts.push('--image', shellQuote(customImage));
      if (customSetup) parts.push('--setup-script', shellQuote(customSetup));
    } else if (imageStrategy === 'install') {
      // Only emit overrides, which keeps the CLI line short in the common case.
      if (selectedAgent && baseImage !== selectedAgent.sandboxed.default_image) {
        parts.push('--image', shellQuote(baseImage));
      }
      if (selectedAgent && setupScript !== selectedAgent.sandboxed.setup) {
        parts.push('--setup-script', shellQuote(setupScript));
      }
    }
    for (const m of mounts) {
      if (!m.host_path || !m.container_path) continue;
      const seg = m.read_only
        ? `${m.host_path}:${m.container_path}:ro`
        : `${m.host_path}:${m.container_path}`;
      parts.push('--mount', shellQuote(seg));
    }
    return parts.join(' ');
  });

  // A fresh folder row, pre-filled at the host home dir (with trailing slash) when known.
  function newMountRow(): FormMount {
    return {
      host_path: homeDir ? homeDir + '/' : '',
      container_path: '/workspace',
      read_only: false,
    };
  }

  function addMount() {
    mounts = [...mounts, newMountRow()];
  }

  function removeMount(i: number) {
    mounts = mounts.filter((_, idx) => idx !== i);
  }

  // The "Use a folder" header toggle: off clears the rows (the session runs with an
  // empty /workspace in the guest), on restores one pre-filled row.
  const useFolder = $derived(mounts.length > 0);
  function toggleFolder() {
    mounts = useFolder ? [] : [newMountRow()];
  }

  // Roll a fresh suggested name (the ↻ button).
  function regenerateName() {
    api
      .suggestName(agent)
      .then((n) => {
        name = n;
        suggestedName = n;
      })
      .catch(() => {});
  }

  function resetForm() {
    name = '';
    mounts = [newMountRow()];
    submitError = null;
    submitting = false;
    pulling = false;
    pullPercent = -1;
    pullStatus = '';
    imagePresent = null;
    customImage = '';
    customSetup = '';
    runtimeChoice = '';
    ownFolder = false;
    workDir = '';
    workspaceFolder = '';
    pickedVMSize = '';
    // baseImage / setupScript / imageStrategy get re-seeded by the agent-change $effect.
  }

  // The image the form is asking for, whichever strategy is selected. Shared by a private session's
  // payload and by the workspace VM it may be creating, since a machine has exactly one image.
  // Only worth asking when this node has a proxy at all.
  let llmProxyUrl = $state('');
  let useLLMProxy = $state(false);
  $effect(() => {
    selectedHostKey; // the setting belongs to the node the session will be created on
    const host = untrack(() => selectedHost);
    llmProxyUrl = '';
    if (!open) return;
    const ac = new AbortController();
    api
      .llmProxy(host, ac.signal)
      .then((p) => (llmProxyUrl = p.url ?? ''))
      .catch(() => {});
    return () => ac.abort();
  });

  function chosenImage(): string | undefined {
    if (imageStrategy === 'official') return selectedAgent?.sandboxed.official_image;
    if (imageStrategy === 'custom') return customImage.trim() || undefined;
    return baseImage || undefined;
  }

  function buildPayload() {
    // A session on the workspace VM takes none of the machine settings: its runtime, size, image and
    // mounts are already decided.
    if (onWorkspace) {
      return {
        agent,
        name: name.trim() || undefined,
        node_id: hostNodes.length > 1 ? nodeId || undefined : undefined,
        on_workspace_vm: true,
        work_dir: workspaceFolderName || undefined,
        use_llm_proxy: llmProxyUrl && useLLMProxy ? true : undefined,
        mounts: [],
      };
    }
    if (onSandboxless) {
      return {
        agent,
        name: name.trim() || undefined,
        node_id: hostNodes.length > 1 ? nodeId || undefined : undefined,
        runtime: effectiveRuntime,
        work_dir: ownFolder ? workDir.trim() : undefined,
        use_llm_proxy: llmProxyUrl && useLLMProxy ? true : undefined,
        mounts: [],
      };
    }
    const payload: {
      agent: string;
      name?: string;
      mounts: MountRequest[];
      image?: string;
      setup_script?: string;
      node_id?: string;
      runtime?: string;
      resources?: SessionResources;
      use_llm_proxy?: boolean;
    } = {
      agent,
      // Send a runtime only while the picker is on screen, which includes a lone No sandbox; otherwise
      // let the node pick its default.
      runtime: showRuntimePicker ? effectiveRuntime || undefined : undefined,
      resources: showsSizeControls ? { size: vmSize } : undefined,
      name: name.trim() || undefined,
      use_llm_proxy: llmProxyUrl && useLLMProxy ? true : undefined,
      // Only send a target when there's a real choice; on a single node the backend creates here.
      node_id: hostNodes.length > 1 ? nodeId || undefined : undefined,
      mounts: mounts
        // Drop incomplete rows; server would 400 anyway.
        .filter((m) => m.host_path && m.container_path)
        .map<MountRequest>((m) => ({
          host_path: m.host_path,
          container_path: m.container_path,
          mode: m.read_only ? 'ro' : 'rw',
        })),
    };
    if (imageStrategy === 'official' && selectedAgent?.sandboxed.official_image) {
      payload.image = selectedAgent.sandboxed.official_image;
    } else if (imageStrategy === 'custom') {
      payload.image = customImage.trim();
      if (customSetup.trim()) payload.setup_script = customSetup;
    } else {
      // install: only emit overrides so registry updates flow into sessions on recreate.
      if (selectedAgent && baseImage !== selectedAgent.sandboxed.default_image) {
        payload.image = baseImage;
      }
      if (selectedAgent && setupScript !== selectedAgent.sandboxed.setup) {
        payload.setup_script = setupScript;
      }
    }
    return payload;
  }

  // The image ref a create will actually use. Mirrors the image
  // resolution in buildPayload (install falls back to the base image).
  function effectiveImage(): string {
    if (imageStrategy === 'official') return selectedAgent?.sandboxed.official_image ?? '';
    if (imageStrategy === 'custom') return customImage.trim();
    return baseImage || selectedAgent?.sandboxed.default_image || '';
  }

  // Proactively check whether the selected image is downloaded so we can
  // show a "will download" hint before the user commits. Debounced because the
  // custom-image field changes per keystroke. Reads the image-strategy
  // inputs so Svelte re-runs this when any of them change.
  $effect(() => {
    const img = effectiveImage();
    // Asking a runtime that cannot host anything whether it has an image just fails.
    const blocked = runtimeBlocked;
    // Read here, not in the timer below: a value only read inside the callback is not a dependency, so
    // switching the runtime would leave the previous one's answer standing.
    const runtime = effectiveRuntime;
    imagePresent = null;
    // Nothing to ask before a runtime is picked, and a runtime with no guest has no image to download.
    if (!img || blocked || !runtime || onSandboxless) return;
    let cancelled = false;
    const timer = setTimeout(async () => {
      try {
        const present = await api.imageExists(img, selectedHost, undefined, runtime);
        if (!cancelled) imagePresent = present;
      } catch {
        // Don't block create on a flaky check; leave it unknown. The VM builds a missing image itself
        // at create.
        if (!cancelled) imagePresent = null;
      }
    }, 400);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  });

  // Download the effective image, streaming progress into the bar.
  async function pullCurrentImage(): Promise<void> {
    pulling = true;
    pullPercent = -1;
    pullStatus = '';
    try {
      await api.pullImageStream(
        effectiveImage(),
        (p) => {
          pullPercent = p.percent;
          pullStatus = p.status;
        },
        selectedHost,
        effectiveRuntime,
      );
      imagePresent = true;
    } finally {
      pulling = false;
    }
  }

  async function submit() {
    if (submitting || pulling) return;
    if (
      showsImageControls &&
      imageStrategy === 'official' &&
      !selectedAgent?.sandboxed.official_image
    )
      return;
    if (showsImageControls && imageStrategy === 'custom' && !customImage.trim()) {
      submitError = {
        title: 'Enter an image reference',
        raw: 'Bring your own image requires an image reference.',
      };
      return;
    }
    submitting = true;
    submitError = null;

    try {
      // The machine has to exist before a session can land on it. Recording it is instant; the VM boots
      // when this session starts, which the session page already shows.
      if (creatingWorkspaceVM) {
        workspaceVM = await api.saveWorkspaceVM(
          {
            host_root: workspaceHostRoot.trim(),
            guest_root: workspaceGuestRoot.trim() || undefined,
            image: chosenImage(),
            // The machine is what installs the agent, so a custom setup belongs to it.
            setup_script: imageStrategy === 'custom' ? customSetup.trim() || undefined : undefined,
            resources: { size: workspaceSize },
          },
          selectedHost,
        );
      }
      // Download first if we know it's absent, so the create itself is fast and we can show a real
      // progress bar. The workspace VM already has its image.
      if (!onWorkspace && !onSandboxless && imagePresent === false) await pullCurrentImage();

      // Create via the boot transport with node_id in the payload: the receiving node routes it (creates
      // locally if it's the target, else proxies to the target over its own tailnet). This works in both
      // connected and dashboard mode without depending on the in-browser WASM node being signed in,
      // unlike host-routing the create, which would hang when WASM isn't up (connected mode).
      const created = await api.createSession(buildPayload());
      sessions.add(created);
      open = false;
      resetForm();
      await goto(`/sessions/${created.id}`);
    } catch (e) {
      const fe = friendlyError(e);
      submitError = fe;
    } finally {
      submitting = false;
      pulling = false;
    }
  }

  async function copyCommand() {
    try {
      await navigator.clipboard.writeText(command);
      copied = true;
      setTimeout(() => (copied = false), 1500);
    } catch {
      /* ignore */
    }
  }
</script>

{#snippet vmSetupLink()}
  <a
    href="/setup?step=vm"
    onclick={(e) => {
      if (opensInNewTab(e)) return;
      e.preventDefault();
      open = false;
      goto('/setup?step=vm', { state: { autostartSetup: true } });
    }}
    class="cursor-pointer underline underline-offset-4 hover:text-foreground"
  >
    Set up
  </a>
{/snippet}

<Dialog bind:open>
  <DialogContent
    class="flex max-h-[90vh] w-[calc(100%-2rem)] max-w-4xl flex-col gap-0 p-0 sm:max-w-4xl"
  >
    <DialogHeader class="border-b border-border px-6 py-4">
      <DialogTitle>New session</DialogTitle>
      <DialogDescription>
        Spin up a coding-agent session on this node. Pick the folder the agent should work in, or
        run without one.
      </DialogDescription>
    </DialogHeader>

    <div class="flex min-h-0 flex-col gap-5 overflow-y-auto px-6 pb-6 pt-4">
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        {#if hostNodes.length > 1}
          <div class="space-y-2">
            <Label for="node">Node</Label>
            <select
              id="node"
              bind:value={nodeId}
              class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
            >
              {#each hostNodes as node (node.stable_node_id)}
                <option value={node.stable_node_id}>
                  {node.hostname}{node.self ? ' (this machine)' : ''}
                </option>
              {/each}
            </select>
            <p class="text-xs text-muted-foreground">The session lives on the machine you pick.</p>
          </div>
        {:else if soleRemoteNode}
          <div class="space-y-2">
            <Label>Node</Label>
            <p class="text-sm">{soleRemoteNode.hostname}</p>
            <p class="text-xs text-muted-foreground">The session lives on that machine.</p>
          </div>
        {/if}
        {#if showRuntimePicker}
          <div class="space-y-2">
            <Label for="runtime">Isolation</Label>
            <select
              id="runtime"
              aria-describedby="runtime-hint"
              value={effectiveRuntime}
              onchange={(e) => (runtimeChoice = e.currentTarget.value)}
              class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
            >
              <!-- The node picks no default when No sandbox is all it has: that has to be your choice. -->
              {#if !effectiveRuntime}
                <option value="" disabled>Choose one</option>
              {/if}
              {#each runtimeOptions as runtimeName (runtimeName)}
                <option value={runtimeName}>{runtimeLabel(runtimeName)}</option>
              {/each}
              {#if sandboxlessNeedsTmux}
                <option value={RUNTIME_SANDBOXLESS} disabled>
                  {runtimeLabel(RUNTIME_SANDBOXLESS)} (needs tmux)
                </option>
              {/if}
            </select>
            <p id="runtime-hint" class="text-xs text-muted-foreground">
              {effectiveRuntime ? runtimeHint(effectiveRuntime) : 'Pick how this session runs.'}
            </p>
            {#if sandboxlessNeedsTmux}
              <p class="text-xs text-muted-foreground">
                No sandbox needs tmux. Install it, then restart deepcove.
              </p>
            {/if}
            {#if vmSetupOffered}
              <p class="text-xs text-muted-foreground">
                Virtual machines aren't set up yet.
                {@render vmSetupLink()}
              </p>
            {:else if remoteVMSetupOffered}
              <p class="text-xs text-muted-foreground">
                Virtual machines aren't set up on {remoteVMSetupOffered.hostname} yet.
                <a
                  href={`/nodes/${remoteVMSetupOffered.stable_node_id}`}
                  onclick={closeUnlessNewTab}
                  class="underline underline-offset-4 hover:text-foreground"
                >
                  Set up
                </a>
              </p>
            {/if}
          </div>
        {/if}
      </div>

      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div class="space-y-2">
          <Label for="session-name">Name</Label>
          <div class="flex gap-2">
            <Input id="session-name" bind:value={name} placeholder="session name" />
            <Button
              type="button"
              variant="outline"
              size="icon"
              class="shrink-0"
              aria-label="Generate a new name"
              onclick={regenerateName}
            >
              <RefreshCw class="size-4" />
            </Button>
          </div>
        </div>
        <div class="space-y-2">
          <Label for="agent">Agent</Label>
          {#if agentsError}
            <div class="text-sm text-destructive">{agentsError}</div>
          {:else}
            <select
              id="agent"
              bind:value={agent}
              class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
            >
              {#each agents as a (a.name)}
                <option value={a.name}>{a.display_name || a.name}</option>
              {/each}
            </select>
          {/if}
          {#if installsTheAgent}
            <p class="text-xs text-muted-foreground">
              Not on that machine yet. Creating this session installs it first.
            </p>
          {/if}
        </div>
      </div>

      {#if waitingForNodeRuntimes}
        <p class="text-xs text-muted-foreground">
          Checking what {selectedHost?.hostname ?? 'that machine'} can run…
        </p>
      {:else if runtimes === null && !nodeRuntimesError}
        <p class="text-xs text-muted-foreground">
          Checking what {soleRemoteNode?.hostname ?? 'this machine'} can run…
        </p>
      {/if}

      {#if onVMRuntime}
        <div class="space-y-2">
          <Label>Machine</Label>
          <div class="grid grid-cols-2 gap-2">
            <button
              type="button"
              class="rounded-md border p-3 text-left text-sm transition-colors hover:bg-accent/50"
              class:border-primary={onWorkspace}
              onclick={() => (placement = 'workspace')}
            >
              <span class="font-medium">Workspace VM</span>
              <span class="mt-0.5 block text-xs text-muted-foreground">
                One VM your sessions use together, each in its own folder.
              </span>
            </button>
            <button
              type="button"
              class="rounded-md border p-3 text-left text-sm transition-colors hover:bg-accent/50"
              class:border-primary={!onWorkspace}
              onclick={() => (placement = 'private')}
            >
              <span class="font-medium">Session VM</span>
              <span class="mt-0.5 block text-xs text-muted-foreground">
                Only this session. You pick the isolation, size and folders.
              </span>
            </button>
          </div>
        </div>
      {/if}

      {#if onWorkspace && !creatingWorkspaceVM}
        <div class="space-y-2">
          <Label>Image</Label>
          <p class="font-mono text-xs text-muted-foreground">{workspaceVM?.image}</p>
          <p class="text-xs text-muted-foreground">
            The workspace VM's own image. Every session on it runs this one.
          </p>
        </div>
      {/if}

      {#if creatingWorkspaceVM}
        <div class="space-y-4 rounded-md border border-dashed border-border p-3">
          <p class="text-sm">
            You don't have a workspace VM yet. This creates one, and your other sessions can use it
            too.
          </p>
          <WorkspaceVMFields
            bind:hostRoot={workspaceHostRoot}
            bind:guestRoot={workspaceGuestRoot}
            bind:size={workspaceSize}
          />
        </div>
      {:else if onWorkspace}
        <div class="space-y-2">
          <Label for="work-dir">Folder</Label>
          <HostPathField id="work-dir" bind:value={workspaceFolder} host={selectedHost} />
          {#if workspaceFolderName === null}
            <p class="text-xs text-destructive">Pick a folder inside {workspaceVM?.host_root}.</p>
          {:else}
            <p class="text-xs text-muted-foreground">
              A folder inside {workspaceVM?.host_root}, which the VM sees at {workspaceVM?.guest_root}.
              Created if it isn't there; leaving it at {workspaceVM?.host_root}/ uses the session
              name.
            </p>
          {/if}
        </div>
      {/if}

      {#if showsSizeControls}
        <VMSizePicker
          title="Session VM size"
          cards={sessionVMSizes}
          selected={vmSize}
          onselect={(name) => (pickedVMSize = name)}
        />
      {/if}

      {#if showsImageControls}
        <div class="space-y-2">
          <Label>Image</Label>
          <div class="space-y-2 rounded-md border border-border p-3">
            <label
              class="flex items-start gap-2.5 text-sm"
              class:cursor-pointer={selectedAgent?.sandboxed.official_image}
              class:cursor-not-allowed={!selectedAgent?.sandboxed.official_image}
              class:opacity-60={!selectedAgent?.sandboxed.official_image}
            >
              <input
                type="radio"
                bind:group={imageStrategy}
                value="official"
                disabled={!selectedAgent?.sandboxed.official_image}
                class="mt-0.5 size-4 border-input disabled:cursor-not-allowed"
              />
              <div class="flex-1">
                <div class="font-medium">Default deepcove image</div>
                <div class="text-xs text-muted-foreground">
                  {#if selectedAgent?.sandboxed.official_image}
                    Prebuilt and ready, the fastest boot. <code>
                      {selectedAgent.sandboxed.official_image}
                    </code>
                  {:else}
                    Not yet published for this agent. Use "Run install script at startup" for now.
                  {/if}
                </div>
              </div>
            </label>

            <label class="flex cursor-pointer items-start gap-2.5 text-sm">
              <input
                type="radio"
                bind:group={imageStrategy}
                value="custom"
                class="mt-0.5 size-4 border-input"
              />
              <div class="flex-1">
                <div class="font-medium">Bring your own image</div>
                <div class="text-xs text-muted-foreground">
                  Use a custom image. Optionally run extra setup on first boot.
                </div>
              </div>
            </label>

            <label class="flex cursor-pointer items-start gap-2.5 text-sm">
              <input
                type="radio"
                bind:group={imageStrategy}
                value="install"
                class="mt-0.5 size-4 border-input"
              />
              <div class="flex-1">
                <div class="font-medium">Run install script at startup</div>
                <div class="text-xs text-muted-foreground">
                  Start from a base image and install the agent on first boot (~15s for Claude).
                </div>
              </div>
            </label>
          </div>

          {#if imageStrategy === 'install'}
            <div class="space-y-3 rounded-md border border-border p-3">
              <div class="space-y-1.5">
                <Label class="text-xs" for="base-image">Base image</Label>
                <Input id="base-image" bind:value={baseImage} placeholder="alpine:3.20" />
              </div>
              <div class="space-y-1.5">
                <Label class="text-xs" for="setup-script">Setup script</Label>
                <textarea
                  id="setup-script"
                  bind:value={setupScript}
                  rows="10"
                  class="flex w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"></textarea>
                <p class="text-xs text-muted-foreground">
                  Runs each time the session starts, so make it idempotent. Checking
                  <code>command -v &lt;bin&gt;</code>
                  before installing is the convention.
                </p>
              </div>
            </div>
          {/if}

          {#if imageStrategy === 'custom'}
            <div class="space-y-3 rounded-md border border-border p-3">
              <div class="space-y-1.5">
                <Label class="text-xs" for="custom-image">Image reference</Label>
                <Input
                  id="custom-image"
                  bind:value={customImage}
                  placeholder="my-registry/my-image:tag"
                />
              </div>
              <div class="space-y-1.5">
                <Label class="text-xs" for="custom-setup">Extra setup script (optional)</Label>
                <textarea
                  id="custom-setup"
                  bind:value={customSetup}
                  rows="6"
                  placeholder="# Leave blank if your image has the agent baked in.&#10;# Add commands here for any extra setup."
                  class="flex w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"></textarea>
              </div>
            </div>
          {/if}
        </div>
      {/if}

      {#if onSandboxless}
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <Label>Folder</Label>
            <div class="flex items-center gap-2">
              <button
                type="button"
                onclick={() => (ownFolder = !ownFolder)}
                class="text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
              >
                Use my own folder
              </button>
              <Switch
                checked={ownFolder}
                onCheckedChange={() => (ownFolder = !ownFolder)}
                aria-label="Use my own folder"
              />
            </div>
          </div>
          {#if ownFolder}
            <HostPathField id="sandboxless-work-dir" bind:value={workDir} host={selectedHost} />
            <p class="text-xs text-muted-foreground">
              The agent works here, with your files. Deleting the session leaves this folder alone.
            </p>
          {:else}
            <p class="text-xs text-muted-foreground">deepcove makes a folder for this session:</p>
            <p class="font-mono text-xs text-muted-foreground">
              {sandboxlessRoot ? sandboxlessRoot + '/<session id>' : 'under deepcove’s data folder'}
            </p>
            <p class="text-xs text-muted-foreground">It is deleted with the session.</p>
          {/if}
        </div>
      {:else if runtimeKnown && !onWorkspace}
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <Label>Folder</Label>
            <div class="flex items-center gap-2">
              <!-- A label cannot activate a button, and the switch is one, so the text toggles itself. -->
              <button
                type="button"
                onclick={toggleFolder}
                class="text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
              >
                Use a folder
              </button>
              <Switch
                checked={useFolder}
                onCheckedChange={toggleFolder}
                aria-label="Use a folder"
              />
            </div>
          </div>
          {#if !useFolder}
            <p class="text-xs text-muted-foreground">
              No folder. The agent starts in an empty <code>/workspace</code>
              . Turn on "Use a folder" to mount your code.
            </p>
          {:else}
            <p class="text-xs text-muted-foreground">
              The agent works in this folder (mounted at <code>/workspace</code>
              ).
            </p>
            {#each mounts as m, i (i)}
              <div class="space-y-2 rounded-md border border-border p-2.5">
                <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
                  <div class="min-w-0 space-y-1 sm:col-span-2">
                    <Label class="text-xs" for="mount-host-{i}">Host path</Label>
                    <HostPathField
                      id="mount-host-{i}"
                      bind:value={m.host_path}
                      host={selectedHost}
                    />
                  </div>
                  <div class="min-w-0 space-y-1">
                    <Label class="text-xs" for="mount-container-{i}">Mounts to</Label>
                    <Input
                      id="mount-container-{i}"
                      bind:value={m.container_path}
                      placeholder="/workspace"
                    />
                  </div>
                </div>
                <div class="flex items-center justify-between">
                  <label class="flex cursor-pointer select-none items-center gap-1.5 text-xs">
                    <input
                      type="checkbox"
                      bind:checked={m.read_only}
                      class="size-3.5 rounded border-input"
                    />
                    Read-only
                  </label>
                  <Button
                    size="sm"
                    variant="ghost"
                    onclick={() => removeMount(i)}
                    aria-label="Remove folder"
                  >
                    <X class="size-4" />
                  </Button>
                </div>
              </div>
            {/each}
            <Button size="sm" variant="ghost" onclick={addMount}>
              <Plus class="size-4" />
              Add another folder
            </Button>
          {/if}
        </div>
      {/if}

      {#if submitError}
        <div class="rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2.5 text-sm">
          <div class="font-medium text-destructive">{submitError.title}</div>
          {#if submitError.guidance}
            <div class="mt-0.5 text-xs text-foreground">{submitError.guidance}</div>
          {/if}
          {#if submitError.raw && submitError.raw !== submitError.title}
            <div class="mt-1 break-words font-mono text-[11px] text-muted-foreground">
              {submitError.raw}
            </div>
          {/if}
        </div>
      {/if}

      {#if (preflightLoaded || nodeRuntimesError) && runtimeBlocked}
        <div
          class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-foreground"
        >
          {runtimeBlocked}
          {#if otherIsolationUsable}
            Pick another option under Isolation to create a session now.
          {/if}
          <!-- With the picker on screen, its own "Virtual machines aren't set up yet" link is enough. -->
          {#if vmSetupOffered && runtimeOptions.length === 0 && !showRuntimePicker}
            {@render vmSetupLink()}
          {/if}
          {#if remoteNodeWithNothing}
            <a
              href={`/nodes/${remoteNodeWithNothing.stable_node_id}`}
              onclick={closeUnlessNewTab}
              class="underline underline-offset-4"
            >
              Set one up on its page under Nodes.
            </a>
          {/if}
          {#if nodeRuntimesError}
            <button
              type="button"
              onclick={() => nodeRuntimesAttempt++}
              class="ml-1 cursor-pointer underline underline-offset-4"
            >
              Retry
            </button>
          {/if}
        </div>
      {:else if pulling}
        <!-- Live download progress. Determinate once the registry reports
             layer sizes; indeterminate (pulsing) before that. -->
        <div class="space-y-1.5 rounded-md border border-border bg-secondary/40 px-3 py-2.5">
          <div class="flex items-center justify-between text-xs">
            <span class="flex items-center gap-1.5 text-foreground">
              <Download class="size-3.5" />
              {pullStatus || 'Downloading image…'}
            </span>
            {#if pullPercent >= 0}
              <span class="font-mono text-muted-foreground">{pullPercent}%</span>
            {/if}
          </div>
          <div class="h-1.5 w-full overflow-hidden rounded-full bg-secondary">
            {#if pullPercent >= 0}
              <div
                class="h-full rounded-full bg-primary transition-[width] duration-300"
                style="width: {pullPercent}%"
              ></div>
            {:else}
              <div class="h-full w-1/3 animate-pulse rounded-full bg-primary"></div>
            {/if}
          </div>
        </div>
      {:else if imagePresent === false && !submitError}
        <!-- Proactive heads-up so the download isn't a surprise on click. -->
        <div
          class="flex items-start gap-2 rounded-md border border-border bg-secondary/40 px-3 py-2 text-xs text-muted-foreground"
        >
          <Download class="mt-0.5 size-3.5 shrink-0" />
          <span>
            This agent image isn't downloaded yet. It downloads once when you create the session,
            then is reused.
          </span>
        </div>
      {/if}

      {#if llmProxyUrl}
        <div class="flex items-start justify-between gap-4">
          <button
            type="button"
            onclick={() => (useLLMProxy = !useLLMProxy)}
            class="space-y-1 text-left"
          >
            <span class="block text-sm font-medium">Use the LLM proxy</span>
            <span class="block text-xs leading-relaxed text-muted-foreground">
              <span class="font-mono">{llmProxyUrl}</span>
              supplies the key. When it's off, the agent uses {loginNote}.
            </span>
          </button>
          <Switch
            bind:checked={useLLMProxy}
            class="mt-0.5 shrink-0"
            aria-label="Use the LLM proxy"
          />
        </div>
      {/if}

      <div class="flex items-center justify-end gap-2">
        <Button variant="ghost" onclick={() => (open = false)} disabled={submitting || pulling}>
          Cancel
        </Button>
        <Button
          onclick={submit}
          disabled={submitting ||
            pulling ||
            waitingForNodeRuntimes ||
            !name.trim() ||
            !!runtimeBlocked ||
            // Nothing is sent until the runtime is known: the sections that depend on it are hidden.
            !runtimeKnown ||
            (onSandboxless && ownFolder && !workDir.trim()) ||
            (onWorkspace && workspaceFolderName === null) ||
            (showsSizeControls && pickedSizeTooBig) ||
            (showsImageControls &&
              imageStrategy === 'official' &&
              !selectedAgent?.sandboxed.official_image) ||
            (showsImageControls && imageStrategy === 'custom' && !customImage.trim())}
        >
          {#if pulling}
            Downloading image…{pullPercent >= 0 ? ` ${pullPercent}%` : ''}
          {:else if submitting}
            Creating…
          {:else if imagePresent === false}
            Download & create
          {:else}
            Create session
          {/if}
        </Button>
      </div>

      {#if runtimeKnown}
        <details class="group rounded-md border border-border">
          <summary
            class="flex cursor-pointer items-center justify-between rounded-md px-3 py-2 text-sm font-medium hover:bg-secondary/50"
          >
            <span>Or run from CLI</span>
            <span class="text-xs text-muted-foreground group-open:hidden">show</span>
            <span class="hidden text-xs text-muted-foreground group-open:inline">hide</span>
          </summary>
          <div class="border-t border-border p-3">
            <div
              class="flex items-start gap-2 rounded-md border border-border bg-secondary p-2.5 font-mono text-xs"
            >
              <code class="flex-1 overflow-x-auto whitespace-pre">{command}</code>
              <Button size="icon" variant="ghost" onclick={copyCommand} aria-label="Copy command">
                {#if copied}
                  <Check class="size-4 text-[var(--color-success)]" />
                {:else}
                  <Copy class="size-4" />
                {/if}
              </Button>
            </div>
            <p class="mt-2 text-xs text-muted-foreground">
              Run on the machine you want to host the session. The new session appears in the
              dashboard once it's running.
            </p>
          </div>
        </details>
      {/if}
    </div>
  </DialogContent>
</Dialog>
