<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
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
  import { api, ApiError, preflightTimeoutMs } from '$lib/api/client';
  import { discoveryRequired } from '$lib/api/transport';
  import { discovery } from '$lib/stores/discovery.svelte';
  import { friendlyError, type FriendlyError } from '$lib/api/errors';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { onlineHostNodes, defaultNodeId, nodeHost } from '$lib/api/sessions';
  import HostPathField from './HostPathField.svelte';
  import VMSizePicker from './VMSizePicker.svelte';
  import LoginModePicker from './LoginModePicker.svelte';
  import WhereItRuns from './WhereItRuns.svelte';
  import WorkspaceVMFields from './WorkspaceVMFields.svelte';
  import { footerClass } from './wizardFooter';
  import { loginChoices, type LoginPlace } from '$lib/util/loginMode';
  import { shellQuote } from '$lib/util/shell';
  import { looksLikePath, pathInsideFolder, sessionFolderIn } from '$lib/util/path';
  import type {
    Agent,
    LoginMode,
    NodeInfo,
    RuntimesInfo,
    SessionResources,
    SharedFolder,
    SharedLoginStatus,
    WorkspaceVM,
  } from '$lib/api/types';
  import {
    findLastPick,
    missingReasons,
    runtimeTiles,
    RUNTIME_SANDBOXLESS,
    type RuntimePlace,
  } from '$lib/util/runtime';
  import {
    canPickVMSize,
    customSessionVMTitleNote,
    DEFAULT_VM_SIZE,
    describeVMSize,
    sessionVMSizeCards,
  } from '$lib/util/vmSize';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import Plus from '@lucide/svelte/icons/plus';
  import X from '@lucide/svelte/icons/x';
  import Download from '@lucide/svelte/icons/download';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';

  let { open = $bindable(false) }: { open?: boolean } = $props();

  // read_only is required here so the checkbox binds to a boolean.
  type FolderRow = Required<SharedFolder>;
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
  // The node the session lands on, by name.
  const targetNodeName = $derived(
    hostNodes.find((node) => node.stable_node_id === nodeId)?.hostname ??
      soleRemoteNode?.hostname ??
      'this node',
  );
  // How your newest session runs, on any node. A new session starts from it, so nothing is stored. Read
  // once a visit, so a session made elsewhere meanwhile can't change what you left untouched here.
  const lastPick = $derived(
    open && !sessions.loading ? untrack(() => findLastPick(sessions.list)) : null,
  );
  // The last pick applies only on its own node. No node on it means this one.
  const isOnLastPickNode = $derived(
    lastPick !== null && (lastPick.nodeId ? lastPick.nodeId === nodeId : !selectedHost),
  );
  // A stable key for that host. selectedHost is a fresh object on every recompute, so an effect that
  // depends on it re-runs forever; the dns name changes only when the choice does.
  const selectedHostKey = $derived(selectedHost?.dns_name ?? '');
  // The last auto-suggested name, so the prefill effect can refresh it on agent
  // change without clobbering a name the user has typed.
  let suggestedName = $state('');
  // Prefer-setting: start with one folder row ready to fill (the conventional
  // /workspace). The user can opt out ("Run without a folder") to clear it.
  let folders = $state<FolderRow[]>([
    { host_path: '', guest_path: '/workspace', read_only: false },
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

  // Each visit starts on the agent of your last session, when this build still has it. The last pick
  // can land after the dialog opens, so it is followed until you pick an agent yourself.
  let isAgentPicked = $state(false);
  $effect(() => {
    if (!open || agents.length === 0) return;
    const lastAgent = lastPick?.agent;
    if (untrack(() => isAgentPicked)) return;
    if (lastAgent && agents.some((known) => known.name === lastAgent)) agent = lastAgent;
  });

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

  // Load the user's nodes on open. Connected mode reads them from the server; web node mode reads the
  // in-browser node's discovery. Best-effort: a failure just hides the selector and creates here.
  $effect(() => {
    if (!open || nodes.length > 0) return;
    if (discoveryRequired()) {
      nodes = discovery.nodes;
      return;
    }
    api
      .nodes()
      .then((list) => (nodes = list))
      .catch(() => {});
  });
  // Each visit starts on your last session's node while it is online, else on this machine. A node you
  // picked yourself stays while it is online.
  let isNodePicked = $state(false);
  const hostNodeIds = $derived(hostNodes.map((node) => node.stable_node_id).join(','));
  $effect(() => {
    if (!open) return;
    void hostNodeIds;
    const lastNodeId = lastPick?.nodeId;
    untrack(() => {
      if (isNodePicked && hostNodes.some((node) => node.stable_node_id === nodeId)) return;
      const lastNode = hostNodes.find((node) => node.stable_node_id === lastNodeId);
      nodeId = lastNode?.stable_node_id ?? defaultNodeId(hostNodes);
    });
  });
  // A pick made yourself lasts this visit only.
  $effect(() => {
    if (open) return;
    isAgentPicked = false;
    isNodePicked = false;
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
  // A pick belongs to the node it was made for, and to this visit: reopening or switching nodes starts
  // from that node's last pick or default. Not on a re-read of the same node, such as the one after a
  // setup, which would drop the VM you just picked.
  $effect(() => {
    void open;
    void selectedHostKey;
    pickedPlace = '';
    pickedVMSize = null;
    draftWorkspaceRoot = '';
    draftWorkspaceGuestRoot = '/workspace';
    draftWorkspaceResources = { size: DEFAULT_VM_SIZE };
    // A download started for the last node is that node's. Its button is gone, so nothing else clears it.
    downloadingVM = false;
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
  const tiles = $derived(runtimes ? runtimeTiles(runtimes) : []);
  // Only the user's explicit pick is stored. Resolving it against the node happens in a $derived
  // below, NOT in an $effect: an effect that both reads and writes the same state re-triggers itself,
  // which in Svelte 5 runs until effect_update_depth_exceeded and hangs the page.
  let pickedPlace = $state<RuntimePlace | ''>('');
  // Your last session's tile, when this node can run it now. No isolation is never carried over, so
  // running without a VM is always a choice made on purpose.
  const lastPickTile = $derived(
    isOnLastPickNode
      ? tiles.find(
          (tile) =>
            tile.place === lastPick?.place &&
            tile.place !== 'no_sandbox' &&
            !tile.blocked &&
            !tile.needsSetup,
        )
      : undefined,
  );
  // The tile you picked, else your last session's, else the node's default. With no usable default,
  // nothing is picked until you choose.
  const pickedTile = $derived(
    tiles.find((tile) => tile.place === pickedPlace) ??
      lastPickTile ??
      tiles.find((tile) => tile.isDefault && !tile.blocked),
  );
  // True while the download runs, so its progress gets the whole row.
  let downloadingVM = $state(false);
  // The node picks its default again once the download is done, and that is the VM, so a fresh read
  // selects it and the form opens up.
  function refreshRuntimes() {
    if (selectedHostKey) nodeRuntimesAttempt++;
    else return preflight.refresh();
  }
  // What the create will actually use. Image checks and pulls target this too, since each runtime keeps
  // its own image store.
  const effectiveRuntime = $derived(pickedTile && !pickedTile.needsSetup ? pickedTile.runtime : '');

  // Nothing to shape the form around yet: the node is still answering or never did, has nothing to run
  // on, or is waiting for you to pick. Until then the runtime-specific sections stay hidden rather than
  // guess.
  const runtimeKnown = $derived(effectiveRuntime !== '');

  // No sandbox means no image and no size caps, and the folder is a real folder on that machine rather
  // than something shared into a guest.
  const onSandboxless = $derived(effectiveRuntime === RUNTIME_SANDBOXLESS);
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
    if (pickedTile?.place === 'workspace_vm' && workspaceVMError) return workspaceVMError;
    if (tiles.length > 0) return pickedTile?.blocked || null;
    const where = (selectedHost ?? soleRemoteNode)?.hostname ?? 'This machine';
    return [`${where} has nothing to run sessions on yet.`, ...missingReasons(runtimes)].join(' ');
  });
  // Another tile could host the session right now.
  const otherRuntimeUsable = $derived(
    tiles.some((tile) => tile.place !== pickedTile?.place && !tile.blocked && !tile.needsSetup),
  );

  let workspaceVM = $state<WorkspaceVM | null>(null);
  // The first shared VM on a node is set up with the session that needs it. These hold its fields
  // until Create saves it.
  let draftWorkspaceRoot = $state('');
  let draftWorkspaceGuestRoot = $state('/workspace');
  let draftWorkspaceResources = $state<SessionResources>({ size: DEFAULT_VM_SIZE });
  // False until the node says whether it has a workspace VM. Without the answer, the dialog can't tell
  // whether to set one up.
  let workspaceVMChecked = $state(false);
  let workspaceVMError = $state<string | null>(null);
  let workDir = $state('');
  const onWorkspace = $derived(pickedTile?.place === 'workspace_vm' && runtimeKnown);
  const needsWorkspaceVM = $derived(onWorkspace && workspaceVMChecked && workspaceVM === null);
  const isWorkspaceSetupReady = $derived(
    looksLikePath(draftWorkspaceRoot) &&
      looksLikePath(draftWorkspaceGuestRoot) &&
      (runtimes?.workspace_vm_sizes?.length ?? 0) > 0,
  );
  const workspaceSharedRoot = $derived(
    workspaceVM?.host_root ?? (needsWorkspaceVM ? draftWorkspaceRoot.trim() : ''),
  );
  const workspaceGuestRoot = $derived(
    workspaceVM?.guest_root ?? (needsWorkspaceVM ? draftWorkspaceGuestRoot.trim() : ''),
  );
  // The session's folder on the workspace VM, as a path on this machine. It follows the name until you
  // change it, so the field always shows the folder the session gets.
  let workspaceFolder = $state('');
  let suggestedWorkspaceFolder = $state('');
  $effect(() => {
    const suggested = looksLikePath(workspaceSharedRoot)
      ? sessionFolderIn(workspaceSharedRoot, name)
      : '';
    untrack(() => {
      if (!workspaceFolder || workspaceFolder === suggestedWorkspaceFolder) {
        workspaceFolder = suggested;
      }
      suggestedWorkspaceFolder = suggested;
    });
  });
  // What the server is sent: a name inside the shared folder, '.' for the shared folder itself, or null
  // when the folder is outside it.
  const workspaceFolderName = $derived.by(() => {
    if (!onWorkspace || !looksLikePath(workspaceSharedRoot)) return '';
    const inside = pathInsideFolder(workspaceSharedRoot, workspaceFolder);
    return inside === '' ? '.' : inside;
  });
  // Where the session's folder shows up in the workspace VM. It follows the folder on the node.
  const workspaceGuestFolder = $derived.by(() => {
    if (!workspaceGuestRoot || !workspaceFolderName) return '';
    if (workspaceFolderName === '.') return workspaceGuestRoot;
    return `${workspaceGuestRoot.replace(/\/$/, '')}/${workspaceFolderName}`;
  });
  // Whether the session's folder is already there. Null until the node answers, or when it can't say.
  let workspaceFolderExists = $state<boolean | null>(null);
  $effect(() => {
    const folder = workspaceFolder.trim();
    const host = untrack(() => selectedHost);
    workspaceFolderExists = null;
    if (!onWorkspace || workspaceFolderName === null || !looksLikePath(folder)) return;
    const ac = new AbortController();
    const timer = setTimeout(() => {
      api
        .fsList(folder, host, ac.signal)
        .then(() => {
          if (!ac.signal.aborted) workspaceFolderExists = true;
        })
        .catch((e) => {
          if (!ac.signal.aborted && e instanceof ApiError && e.status === 404) {
            workspaceFolderExists = false;
          }
        });
    }, 200);
    return () => {
      clearTimeout(timer);
      ac.abort();
    };
  });
  // Which controls this session shows. If a control is hidden, it cannot stop you from creating the
  // session, so the Create button checks these too.
  const showsImageControls = $derived(runtimeKnown && !onWorkspace && !onSandboxless);
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
    workspaceVMChecked = false;
    workspaceVMError = null;
    api
      .workspaceVM(host, ac.signal)
      .then((vm) => {
        if (ac.signal.aborted) return;
        workspaceVM = vm;
        workspaceVMChecked = true;
      })
      .catch(() => {
        if (!ac.signal.aborted) {
          workspaceVMError = `Couldn't check whether ${host?.hostname ?? 'this node'} has a workspace VM.`;
        }
      });
    return () => ac.abort();
  });

  // The size you picked, else your last session's when this node can give it, else the node's default.
  let pickedVMSize = $state<SessionResources | null>(null);
  const sessionVMSizes = $derived(sessionVMSizeCards(runtimes?.session_vm_sizes ?? []));
  const lastPickVMSize = $derived.by(() => {
    if (!lastPick || !isOnLastPickNode || lastPick.place !== 'session_vm') return null;
    const { size, cpu, memory_mb } = lastPick.resources;
    const resources = size ? { size } : { cpu, memory_mb };
    return canPickVMSize(resources, sessionVMSizes, runtimes?.vm_limits) ? resources : null;
  });
  const vmSize = $derived(
    pickedVMSize ?? lastPickVMSize ?? { size: runtimes?.default_vm_size || DEFAULT_VM_SIZE },
  );
  const isFromLastSession = $derived(
    lastPickTile !== undefined &&
      pickedTile === lastPickTile &&
      pickedPlace === '' &&
      pickedVMSize === null &&
      (lastPickTile.place !== 'session_vm' || lastPickVMSize !== null),
  );
  // What the row says about size: the session's own VM, or the shared VM it joins.
  const sizeSummary = $derived.by(() => {
    if (onWorkspace) return workspaceVM ? describeVMSize(workspaceVM.resources, undefined) : '';
    return showsSizeControls ? describeVMSize(vmSize, undefined) : '';
  });

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
          const first = folders[0];
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
    if (effectiveRuntime) parts.push('--runtime', effectiveRuntime);
    if (onSandboxless) {
      // No image, no setup script and no shared folders on this runtime, so the folder is all that is left.
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
    for (const folder of folders) {
      if (!folder.host_path || !folder.guest_path) continue;
      const seg = folder.read_only
        ? `${folder.host_path}:${folder.guest_path}:ro`
        : `${folder.host_path}:${folder.guest_path}`;
      parts.push('--share', shellQuote(seg));
    }
    return parts.join(' ');
  });

  // A fresh folder row, pre-filled at the host home dir (with trailing slash) when known. Only the work
  // folder starts at /workspace, since a second folder there would collide with it.
  function newFolderRow(guestPath: string): FolderRow {
    return {
      host_path: homeDir ? homeDir + '/' : '',
      guest_path: guestPath,
      read_only: false,
    };
  }

  function addFolder() {
    folders = [...folders, newFolderRow('')];
  }

  function removeFolder(i: number) {
    folders = folders.filter((_, idx) => idx !== i);
  }

  // The work folder's place in the VM shows as a line until you edit it.
  let isEditingGuestFolder = $state(false);

  // The first row is the work folder, where the agent starts. An empty one means no folder, and then
  // a more folder would become the work folder, so that is refused.
  const workFolder = $derived(folders[0]);
  const moreFoldersWithoutWorkFolder = $derived(
    !workFolder?.host_path.trim() && folders.slice(1).some((folder) => folder.host_path.trim()),
  );
  const workFolderGuestInvalid = $derived(
    !!workFolder?.host_path.trim() && !looksLikePath(workFolder.guest_path),
  );
  // Each more folder needs its own place in the VM. Left empty, the row would be dropped without a word.
  const moreFolderGuestMissing = $derived(
    folders.slice(1).some((folder) => folder.host_path.trim() && !looksLikePath(folder.guest_path)),
  );

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
    folders = [newFolderRow('/workspace')];
    submitError = null;
    submitting = false;
    pulling = false;
    pullPercent = -1;
    pullStatus = '';
    imagePresent = null;
    customImage = '';
    customSetup = '';
    pickedPlace = '';
    ownFolder = false;
    workDir = '';
    workspaceFolder = '';
    pickedVMSize = null;
    isEditingGuestFolder = false;
    // baseImage / setupScript / imageStrategy get re-seeded by the agent-change $effect.
  }

  // The LLM proxy is offered only when this node has one.
  let llmProxyUrl = $state('');
  // The picker waits for this, or an LLM proxy default would be sent as the session's own login.
  let llmProxyChecked = $state(false);
  $effect(() => {
    selectedHostKey; // the setting belongs to the node the session will be created on
    const host = untrack(() => selectedHost);
    llmProxyUrl = '';
    llmProxyChecked = false;
    if (!open) return;
    const ac = new AbortController();
    api
      .llmProxy(host, ac.signal)
      .then((p) => {
        if (!ac.signal.aborted) llmProxyUrl = p.url ?? '';
      })
      .catch(() => {})
      .finally(() => {
        if (!ac.signal.aborted) llmProxyChecked = true;
      });
    return () => ac.abort();
  });

  // Seeded from the default of the node the session will be created on, whenever the agent or the node
  // changes.
  let loginMode = $state<LoginMode>('shared');
  let loginSetup = $state<SharedLoginStatus | null>(null);
  $effect(() => {
    selectedHostKey;
    const host = untrack(() => selectedHost);
    const currentAgent = agent;
    loginSetup = null;
    if (!open || !currentAgent) return;
    const ac = new AbortController();
    api
      .sharedLogin(currentAgent, host, ac.signal)
      .then((status) => {
        // A node picked since then has its own answer coming.
        if (ac.signal.aborted) return;
        loginSetup = status;
        loginMode = status.default_login_mode;
      })
      .catch(() => {});
    return () => ac.abort();
  });
  const loginPlace = $derived<LoginPlace>(
    onWorkspace ? 'workspace_vm' : onSandboxless ? 'no_sandbox' : 'session_vm',
  );
  // Until both answers are in, no mode is sent and the node uses its default.
  const loginChoicesHere = $derived(
    loginSetup && llmProxyChecked && selectedAgent
      ? loginChoices(selectedAgent, loginPlace, {
          proxyUrl: llmProxyUrl,
          hasApiKey: loginSetup.has_api_key,
          apiKeyEnding: loginSetup.api_key_ending,
        })
      : [],
  );
  // A default this session cannot use becomes its own login, which is what the node does too.
  const pickedLogin = $derived.by(() => {
    const usable = loginChoicesHere.filter((choice) => !choice.unavailable);
    return (
      usable.find((choice) => choice.mode === loginMode)?.mode ??
      usable.find((choice) => choice.mode === 'in_session')?.mode ??
      usable[0]?.mode
    );
  });

  function buildPayload() {
    // A session on the workspace VM takes none of the machine settings: its runtime, size, image and
    // shared folder are already decided.
    if (onWorkspace) {
      return {
        agent,
        name: name.trim() || undefined,
        node_id: hostNodes.length > 1 ? nodeId || undefined : undefined,
        on_workspace_vm: true,
        work_dir: workspaceFolderName || undefined,
        login_mode: pickedLogin,
        shared_folders: [],
      };
    }
    if (onSandboxless) {
      return {
        agent,
        name: name.trim() || undefined,
        node_id: hostNodes.length > 1 ? nodeId || undefined : undefined,
        runtime: effectiveRuntime,
        work_dir: ownFolder ? workDir.trim() : undefined,
        login_mode: pickedLogin,
        shared_folders: [],
      };
    }
    const payload: {
      agent: string;
      name?: string;
      shared_folders: SharedFolder[];
      image?: string;
      setup_script?: string;
      node_id?: string;
      runtime?: string;
      resources?: SessionResources;
      login_mode?: LoginMode;
    } = {
      agent,
      runtime: effectiveRuntime || undefined,
      resources: showsSizeControls ? vmSize : undefined,
      name: name.trim() || undefined,
      login_mode: pickedLogin,
      // Only send a target when there's a real choice; on a single node the backend creates here.
      node_id: hostNodes.length > 1 ? nodeId || undefined : undefined,
      // Drop incomplete rows; server would 400 anyway.
      shared_folders: folders.filter((folder) => folder.host_path && folder.guest_path),
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
    // Read before anything is awaited: closing the dialog resets the form while this runs.
    const payload = buildPayload();
    const host = selectedHost;
    const isImageMissing = !onWorkspace && !onSandboxless && imagePresent === false;

    try {
      // Saved first, so the session's folder is inside the folder the VM can see.
      if (needsWorkspaceVM) {
        workspaceVM = await api.saveWorkspaceVM(
          {
            host_root: draftWorkspaceRoot.trim(),
            guest_root: draftWorkspaceGuestRoot.trim(),
            resources: draftWorkspaceResources,
          },
          host,
        );
      }
      // Download first if we know it's absent, so the create itself is fast and we can show a real
      // progress bar. The workspace VM already has its image.
      if (isImageMissing) await pullCurrentImage();

      // Create via the boot transport with node_id in the payload: the receiving node routes it (creates
      // locally if it's the target, else proxies to the target over its own tailnet). This works in both
      // connected and web node mode without depending on the in-browser WASM node being signed in,
      // unlike host-routing the create, which would hang when WASM isn't up (connected mode).
      const created = await api.createSession(payload);
      // The list tags each session with its node, and this one is new to it.
      sessions.add(host ? { ...created, host } : created);
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

<Dialog bind:open>
  <DialogContent
    class="flex h-dvh max-h-dvh w-full max-w-full flex-col gap-0 rounded-none p-0 sm:h-auto sm:max-h-[90vh] sm:w-[calc(100%-2rem)] sm:max-w-2xl sm:rounded-xl"
  >
    <DialogHeader class="border-b border-border py-4 pl-4 pr-12 sm:pl-6">
      <DialogTitle>New session</DialogTitle>
      <DialogDescription class="sr-only">
        Pick where the session runs, then fill in the details.
      </DialogDescription>
    </DialogHeader>

    <div class="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-4 pt-4 sm:px-6 sm:pb-6">
      <WhereItRuns
        nodes={hostNodes}
        {nodeId}
        onpicknode={(stableNodeId) => {
          nodeId = stableNodeId;
          isNodePicked = true;
        }}
        nodeName={targetNodeName}
        host={selectedHost}
        {tiles}
        picked={pickedTile}
        onpickplace={(place) => (pickedPlace = place)}
        isChecking={waitingForNodeRuntimes || (runtimes === null && !nodeRuntimesError)}
        {isFromLastSession}
        {sizeSummary}
        isForcedOpen={!!runtimeBlocked || !!pickedTile?.needsSetup || needsWorkspaceVM}
        isCreating={submitting || pulling}
        onsetupdone={refreshRuntimes}
        bind:downloading={downloadingVM}
        sizeControls={showsSizeControls ? sessionSizeControls : undefined}
        workspaceSetup={needsWorkspaceVM ? firstWorkspaceVMSetup : undefined}
      />

      {#snippet sessionSizeControls()}
        <VMSizePicker
          title="Size"
          cards={sessionVMSizes}
          selected={vmSize}
          onselect={(picked) => (pickedVMSize = picked)}
          limits={runtimes?.vm_limits}
          customTitleNote={(memoryMB) =>
            customSessionVMTitleNote(memoryMB, runtimes?.spare_memory_mb)}
        />
      {/snippet}

      {#snippet firstWorkspaceVMSetup()}
        <div class="space-y-3 border-t border-border pt-3">
          <p class="text-sm">
            This is the first shared VM on {targetNodeName}. It's set up with this session, once.
          </p>
          <WorkspaceVMFields
            bind:hostRoot={draftWorkspaceRoot}
            bind:guestRoot={draftWorkspaceGuestRoot}
            bind:resources={draftWorkspaceResources}
            host={selectedHost}
            sizes={runtimes?.workspace_vm_sizes}
            limits={runtimes?.vm_limits}
          />
        </div>
      {/snippet}

      {#if runtimeKnown}
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <Label for="agent">Agent</Label>
            {#if agentsError}
              <div class="text-sm text-destructive">{agentsError}</div>
            {:else}
              <select
                id="agent"
                bind:value={agent}
                onchange={() => (isAgentPicked = true)}
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
        </div>

        {#if onWorkspace}
          <div class="space-y-2">
            <Label for="work-dir">Work folder</Label>
            <HostPathField
              id="work-dir"
              bind:value={workspaceFolder}
              host={selectedHost}
              startsEmpty
              disabled={!looksLikePath(workspaceSharedRoot)}
            />
            {#if !looksLikePath(workspaceSharedRoot)}
              <p class="text-xs text-muted-foreground">
                Pick the folder the shared VM can see first.
              </p>
            {:else if workspaceFolderName === null}
              <p class="text-xs text-destructive">Must be inside {workspaceSharedRoot}.</p>
            {:else}
              <p class="text-xs text-muted-foreground">
                Must be inside {workspaceSharedRoot}.
                {#if workspaceFolderExists !== null}
                  {workspaceFolderExists ? 'Existing folder.' : 'New folder.'}
                {/if}
                In the VM:
                <span class="font-mono text-foreground">{workspaceGuestFolder}</span>
              </p>
            {/if}
          </div>
        {:else if onSandboxless}
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <Label>Work folder</Label>
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
                The agent works here, with your files. Deleting the session leaves this folder
                alone.
              </p>
            {:else}
              <p class="text-xs text-muted-foreground">deepcove makes a folder for this session:</p>
              <p class="font-mono text-xs text-muted-foreground">
                {sandboxlessRoot
                  ? sandboxlessRoot + '/<session id>'
                  : 'under deepcove’s data folder'}
              </p>
              <p class="text-xs text-muted-foreground">It is deleted with the session.</p>
            {/if}
          </div>
        {:else if workFolder}
          <div class="space-y-2">
            <Label for="work-folder-host">Work folder</Label>
            <HostPathField
              id="work-folder-host"
              bind:value={workFolder.host_path}
              host={selectedHost}
            />
            {#if isEditingGuestFolder}
              <div class="flex items-center gap-2.5">
                <Label
                  class="shrink-0 text-xs font-normal text-muted-foreground"
                  for="work-folder-guest"
                >
                  In the VM
                </Label>
                <Input
                  id="work-folder-guest"
                  bind:value={workFolder.guest_path}
                  placeholder="/workspace"
                />
              </div>
            {/if}
            {#if workFolderGuestInvalid}
              <p class="text-xs text-destructive">
                The folder in the VM needs a full path, starting with /.
              </p>
            {:else if !workFolder.host_path.trim()}
              <p class="text-xs text-muted-foreground">
                No folder. The agent starts in an empty folder in the VM.
              </p>
            {:else if isEditingGuestFolder}
              <p class="text-xs text-muted-foreground">The agent edits your real files.</p>
            {:else}
              <p class="flex flex-wrap items-center gap-x-1.5 text-xs text-muted-foreground">
                In the VM:
                <span class="font-mono text-foreground">{workFolder.guest_path}</span>
                <button
                  type="button"
                  onclick={() => (isEditingGuestFolder = true)}
                  class="px-0.5 py-1 text-primary underline-offset-4 hover:underline"
                >
                  Edit
                </button>
                <span aria-hidden="true">·</span>
                The agent edits your real files.
              </p>
            {/if}
          </div>
        {/if}

        {#if loginChoicesHere.length > 1 && pickedLogin}
          <LoginModePicker
            choices={loginChoicesHere}
            value={pickedLogin}
            defaultMode={loginSetup?.default_login_mode}
            onpick={(mode) => (loginMode = mode)}
          />
        {/if}

        {#if onWorkspace && !needsWorkspaceVM}
          <p class="text-xs text-muted-foreground">
            Size and image come from the workspace VM. Change them on the Nodes page.
          </p>
        {/if}

        {#if runtimeKnown}
          <details class="group rounded-md border border-border">
            <summary
              class="flex cursor-pointer items-center gap-2 rounded-md px-3 py-2 text-sm hover:bg-secondary/50"
            >
              <ChevronRight
                class="size-4 text-muted-foreground transition-transform group-open:rotate-90"
              />
              <span class="font-medium">More options</span>
              <span class="text-xs text-muted-foreground">
                {showsSizeControls ? 'Image, more folders, CLI command' : 'CLI command'}
              </span>
            </summary>
            <div class="space-y-5 border-t border-border p-3">
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
                            Not yet published for this agent. Use "Run install script at startup"
                            for now.
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
                          Start from a base image and install the agent on first boot (~15s).
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
                        <Label class="text-xs" for="custom-setup">
                          Extra setup script (optional)
                        </Label>
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

              {#if showsSizeControls}
                <div class="space-y-2">
                  <Label>More folders</Label>
                  <p class="text-xs text-muted-foreground">
                    Other folders the agent can see, besides the work folder.
                  </p>
                  {#each folders as folder, i (i)}
                    {#if i > 0}
                      <div class="space-y-2 rounded-md border border-border p-2.5">
                        <div class="grid grid-cols-1 gap-2 sm:grid-cols-5">
                          <div class="min-w-0 space-y-1 sm:col-span-3">
                            <Label class="text-xs" for="folder-host-{i}">On {targetNodeName}</Label>
                            <HostPathField
                              id="folder-host-{i}"
                              bind:value={folder.host_path}
                              host={selectedHost}
                            />
                          </div>
                          <div class="min-w-0 space-y-1 sm:col-span-2">
                            <Label class="text-xs" for="folder-guest-{i}">In the VM</Label>
                            <Input
                              id="folder-guest-{i}"
                              bind:value={folder.guest_path}
                              placeholder="/mnt/data"
                            />
                          </div>
                        </div>
                        <div class="flex items-center justify-between">
                          <label
                            class="flex cursor-pointer select-none items-center gap-1.5 text-xs"
                          >
                            <input
                              type="checkbox"
                              bind:checked={folder.read_only}
                              class="size-3.5 rounded border-input"
                            />
                            Read-only
                          </label>
                          <Button
                            size="sm"
                            variant="ghost"
                            onclick={() => removeFolder(i)}
                            aria-label="Remove folder"
                          >
                            <X class="size-4" />
                          </Button>
                        </div>
                      </div>
                    {/if}
                  {/each}
                  {#if moreFolderGuestMissing}
                    <p class="text-xs text-destructive">
                      Each folder needs a path in the VM, starting with /.
                    </p>
                  {/if}
                  {#if moreFoldersWithoutWorkFolder}
                    <p class="text-xs text-destructive">
                      Pick a work folder first. The agent would start in one of these instead.
                    </p>
                  {/if}
                  <Button size="sm" variant="ghost" onclick={addFolder}>
                    <Plus class="size-4" />
                    Add a folder
                  </Button>
                </div>
              {/if}

              <div class="space-y-2">
                <Label>CLI command</Label>
                <div
                  class="flex items-center gap-2 rounded-md border border-border bg-secondary py-1.5 pl-3 pr-1.5 font-mono text-xs"
                >
                  <code class="min-w-0 flex-1 [overflow-wrap:anywhere]">
                    {command}
                  </code>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    class="shrink-0"
                    onclick={copyCommand}
                    aria-label="Copy command"
                  >
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
            </div>
          </details>
        {/if}
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
          {#if otherRuntimeUsable}
            Pick another runtime to create a session now.
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

      <div class={footerClass}>
        <Button
          variant="ghost"
          class="max-sm:hidden"
          onclick={() => (open = false)}
          disabled={downloadingVM}
        >
          Cancel
        </Button>
        <Button
          onclick={submit}
          disabled={submitting ||
            pulling ||
            downloadingVM ||
            waitingForNodeRuntimes ||
            !name.trim() ||
            !!runtimeBlocked ||
            // Nothing is sent until the runtime is known: the sections that depend on it are hidden.
            !runtimeKnown ||
            (onSandboxless && ownFolder && !workDir.trim()) ||
            (onWorkspace && !workspaceVMChecked) ||
            (needsWorkspaceVM && !isWorkspaceSetupReady) ||
            (onWorkspace &&
              (workspaceFolderName === null || !looksLikePath(workspaceSharedRoot))) ||
            (showsSizeControls &&
              (moreFoldersWithoutWorkFolder || workFolderGuestInvalid || moreFolderGuestMissing)) ||
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
    </div>
  </DialogContent>
</Dialog>
