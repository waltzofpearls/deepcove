<script lang="ts">
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
  import { api } from '$lib/api/client';
  import { discoveryRequired } from '$lib/api/transport';
  import { discovery } from '$lib/stores/discovery.svelte';
  import { friendlyError, type FriendlyError } from '$lib/api/errors';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { onlineHostNodes, defaultNodeId, nodeHost } from '$lib/api/sessions';
  import HostPathField from './HostPathField.svelte';
  import { shellQuote } from '$lib/util/shell';
  import type {
    Agent,
    DockerStatus,
    MountRequest,
    NodeInfo,
    RuntimesInfo,
    SessionResources,
  } from '$lib/api/types';
  import { runtimeCapsDisk, runtimeHint, runtimeLabel } from '$lib/util/runtime';
  import { emptyResourceFields, fieldsFromDefaults, parseResources } from '$lib/util/resources';
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
  // Download state while pulling: percent is -1 until the daemon reports
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

  // Docker readiness of the *selected* create target. When a specific node is chosen (multi-node), we
  // gate on that node's Docker, not the primary's. Otherwise we'd let you create on a node whose
  // Docker is down, or block you on the primary's when the target is fine. Null until fetched / when
  // there's no distinct selection, in which case we fall back to the global preflight store below.
  let nodeDocker = $state<DockerStatus | null>(null);
  // Which runtimes the create target offers. Runtime availability is a property of the node (a Linux
  // node will never offer Apple VF), so this is fetched from the selected node alongside its Docker
  // status rather than assumed from the node serving this page.
  let nodeRuntimes = $state<RuntimesInfo | null>(null);
  $effect(() => {
    const host = selectedHost;
    nodeDocker = null;
    nodeRuntimes = null;
    if (!open || !host) return;
    let cancelled = false;
    const ac = new AbortController();
    api
      .preflight(host, ac.signal)
      .then((p) => {
        if (cancelled) return;
        nodeDocker = p.docker;
        nodeRuntimes = p.runtimes;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      ac.abort();
    };
  });

  // Effective Docker readiness the form gates on: the selected node's when known, else the global
  // (primary/local) preflight store.
  const dockerReachable = $derived(nodeDocker ? nodeDocker.reachable : preflight.dockerReachable);
  const dockerLoaded = $derived(nodeDocker !== null || preflight.loaded);
  const dockerMessage = $derived(nodeDocker ? nodeDocker.message : preflight.docker.message);

  // The runtimes the create target offers: the selected node's when known, else this node's. Older
  // nodes predate the field, so treat a missing list as "docker only" rather than showing nothing.
  const runtimes = $derived(nodeRuntimes ?? preflight.runtimes);
  const runtimeOptions = $derived(runtimes?.available ?? []);
  // Only worth showing a picker when there is an actual choice to make.
  const showRuntimePicker = $derived(runtimeOptions.length > 1);
  // Only the user's explicit pick is stored. Resolving it against the node happens in a $derived
  // below, NOT in an $effect: an effect that both reads and writes the same state re-triggers itself,
  // which in Svelte 5 runs until effect_update_depth_exceeded and hangs the page.
  let runtimeChoice = $state('');
  // What the create will actually use. A pick the target node does not offer falls back to that node's
  // default, so switching nodes can never leave an unavailable runtime selected. Image checks and pulls
  // target this too, since each runtime keeps its own image store.
  const effectiveRuntime = $derived.by(() => {
    if (runtimeChoice && runtimeOptions.includes(runtimeChoice)) return runtimeChoice;
    return runtimes?.default ?? '';
  });

  // Why the SELECTED runtime cannot host a session, or null when it can. Docker being down says nothing
  // about the VM runtime, so this is asked per runtime rather than treating Docker as a prerequisite.
  const runtimeBlocked = $derived(runtimes?.unavailable?.[effectiveRuntime] ?? null);
  // Every runtime this node has is down, so nothing can be created here at all.
  const noRuntimeUsable = $derived(
    runtimeOptions.length > 0 &&
      runtimeOptions.every((name) => Boolean(runtimes?.unavailable?.[name])),
  );

  // Size of the machine the session runs on. Blank fields mean "whatever the runtime defaults to",
  // which is what every session got before these inputs existed.
  let resourceFields = $state({ ...emptyResourceFields });
  // Docker ignores a disk cap, so only offer the field where it does something.
  const showDiskField = $derived(runtimeCapsDisk(effectiveRuntime));
  const parsedResources = $derived(parseResources(resourceFields, showDiskField));
  // What this runtime gives a session that asks for nothing. Keyed as a string, not held as the object
  // itself: the preflight poll hands back a fresh object every tick, and an effect that depended on the
  // reference would re-seed the form underneath the user on every poll.
  const defaultsKey = $derived(JSON.stringify(runtimes?.defaults?.[effectiveRuntime] ?? null));
  // Set once the user edits any size field, so re-seeding can never discard what they typed.
  let sizeEdited = $state(false);

  // Seed the size fields with this runtime's real defaults, so the numbers are visible AND editable
  // rather than hidden behind a placeholder. Re-seeds when the runtime changes, because a VM's defaults
  // are not a container's. Reads only defaultsKey and sizeEdited, never resourceFields, so it cannot
  // retrigger itself into effect_update_depth_exceeded.
  $effect(() => {
    const key = defaultsKey;
    if (sizeEdited) return;
    resourceFields = fieldsFromDefaults(JSON.parse(key));
  });
  // Naming the runtime makes "default" mean something: a VM's defaults are not a container's.
  const sizeHint = $derived(
    showDiskField
      ? "Greyed values are what you get if you leave a field blank. Disk caps the writable layer, which lives in the VM's memory."
      : 'Greyed values are what you get if you leave a field blank.',
  );

  // Resolve the host's home dir once, then seed the default folder row's host
  // path so it opens pre-filled at $HOME/ rather than blank.
  $effect(() => {
    if (!open || homeDir) return;
    api
      .fsList(undefined, selectedHost)
      .then((res) => {
        homeDir = res.path;
        if (mounts.length > 0 && !mounts[0].host_path) {
          mounts[0].host_path = res.path + '/';
        }
      })
      .catch(() => {});
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
    baseImage = selectedAgent.default_image;
    setupScript = selectedAgent.default_setup;
    imageStrategy = selectedAgent.official_image ? 'official' : 'install';
  });

  const command = $derived.by(() => {
    const parts = ['deepcove', 'session', agent];
    const trimmedName = name.trim();
    if (trimmedName) parts.push('--name', shellQuote(trimmedName));
    if (imageStrategy === 'official' && selectedAgent?.official_image) {
      parts.push('--image', shellQuote(selectedAgent.official_image));
    } else if (imageStrategy === 'custom' && customImage) {
      parts.push('--image', shellQuote(customImage));
      if (customSetup) parts.push('--setup-script', shellQuote(customSetup));
    } else if (imageStrategy === 'install') {
      // Only emit overrides, which keeps the CLI line short in the common case.
      if (selectedAgent && baseImage !== selectedAgent.default_image) {
        parts.push('--image', shellQuote(baseImage));
      }
      if (selectedAgent && setupScript !== selectedAgent.default_setup) {
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
  // empty in-container /workspace), on restores one pre-filled row.
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
    resourceFields = { ...emptyResourceFields };
    // Let the defaults seed again on the next open, rather than keeping the last session's sizes.
    sizeEdited = false;
    // baseImage / setupScript / imageStrategy get re-seeded by the agent-change $effect.
  }

  function buildPayload() {
    const payload: {
      agent: string;
      name?: string;
      mounts: MountRequest[];
      image?: string;
      setup_script?: string;
      node_id?: string;
      runtime?: string;
      resources?: SessionResources;
    } = {
      agent,
      // Only send a runtime when the user had a real choice; otherwise let the node pick its default.
      runtime: showRuntimePicker ? effectiveRuntime || undefined : undefined,
      resources: parsedResources.resources,
      name: name.trim() || undefined,
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
    if (imageStrategy === 'official' && selectedAgent?.official_image) {
      payload.image = selectedAgent.official_image;
    } else if (imageStrategy === 'custom') {
      payload.image = customImage.trim();
      if (customSetup.trim()) payload.setup_script = customSetup;
    } else {
      // install: only emit overrides so registry updates flow into sessions on recreate.
      if (selectedAgent && baseImage !== selectedAgent.default_image) {
        payload.image = baseImage;
      }
      if (selectedAgent && setupScript !== selectedAgent.default_setup) {
        payload.setup_script = setupScript;
      }
    }
    return payload;
  }

  // The image ref a create will actually use. Mirrors the image
  // resolution in buildPayload (install falls back to the base image).
  function effectiveImage(): string {
    if (imageStrategy === 'official') return selectedAgent?.official_image ?? '';
    if (imageStrategy === 'custom') return customImage.trim();
    return baseImage || selectedAgent?.default_image || '';
  }

  // Proactively check whether the selected image is downloaded so we can
  // show a "will download" hint before the user commits. Debounced because the
  // custom-image field changes per keystroke. Reads the image-strategy
  // inputs so Svelte re-runs this when any of them change.
  $effect(() => {
    const img = effectiveImage();
    const reachable = dockerReachable;
    imagePresent = null;
    if (!img || !reachable) return;
    let cancelled = false;
    const timer = setTimeout(async () => {
      try {
        const present = await api.imageExists(img, selectedHost, undefined, effectiveRuntime);
        if (!cancelled) imagePresent = present;
      } catch {
        // Don't block create on a flaky check; leave it unknown. The
        // create path still pulls on demand via the 422 fallback.
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
    if (imageStrategy === 'official' && !selectedAgent?.official_image) return;
    if (imageStrategy === 'custom' && !customImage.trim()) {
      submitError = {
        title: 'Enter an image reference',
        raw: 'Bring your own image requires an image reference.',
      };
      return;
    }
    submitting = true;
    submitError = null;

    try {
      // Download first if we know it's absent, so the create itself is
      // fast and we can show a real progress bar.
      if (imagePresent === false) await pullCurrentImage();

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
      // Race fallback: the image vanished or wasn't detected up front.
      // Flag it absent so the hint + next create retry will download it.
      if (fe.code === 'image_not_found') imagePresent = false;
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
  <DialogContent class="max-h-[90vh] w-[calc(100%-2rem)] max-w-4xl gap-0 p-0 sm:max-w-4xl">
    <DialogHeader class="border-b border-border px-6 py-4">
      <DialogTitle>New session</DialogTitle>
      <DialogDescription>
        Spin up a coding-agent sandbox on this node. Pick the folder the agent should work in, or
        run without one.
      </DialogDescription>
    </DialogHeader>

    <div class="flex flex-col gap-5 overflow-y-auto px-6 pb-6 pt-4">
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
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

      {#if hostNodes.length > 1}
        <div class="space-y-2">
          <Label for="node">Run on</Label>
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
          <p class="text-xs text-muted-foreground">
            The session and its container live on the machine you pick.
          </p>
        </div>
      {/if}

      {#if showRuntimePicker}
        <div class="space-y-2">
          <Label for="runtime">Isolation</Label>
          <select
            id="runtime"
            value={effectiveRuntime}
            onchange={(e) => (runtimeChoice = e.currentTarget.value)}
            class="flex h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
          >
            {#each runtimeOptions as name (name)}
              <option value={name}>{runtimeLabel(name)}</option>
            {/each}
          </select>
          <p class="text-xs text-muted-foreground">{runtimeHint(effectiveRuntime)}</p>
        </div>
      {/if}

      <div class="space-y-2">
        <Label>Size</Label>
        <div
          class="grid gap-3"
          class:grid-cols-3={showDiskField}
          class:grid-cols-2={!showDiskField}
        >
          <div class="space-y-1.5">
            <Label for="cpu" class="text-xs font-normal text-muted-foreground">CPU cores</Label>
            <Input
              id="cpu"
              type="number"
              min="1"
              step="1"
              placeholder="No limit"
              bind:value={resourceFields.cpu}
              oninput={() => (sizeEdited = true)}
            />
          </div>
          <div class="space-y-1.5">
            <Label for="memory" class="text-xs font-normal text-muted-foreground"
              >Memory (GiB)</Label
            >
            <Input
              id="memory"
              type="number"
              min="0.5"
              step="0.5"
              placeholder="No limit"
              bind:value={resourceFields.memoryGiB}
              oninput={() => (sizeEdited = true)}
            />
          </div>
          {#if showDiskField}
            <div class="space-y-1.5">
              <Label for="disk" class="text-xs font-normal text-muted-foreground">Disk (GiB)</Label>
              <Input
                id="disk"
                type="number"
                min="0.5"
                step="0.5"
                placeholder="No limit"
                bind:value={resourceFields.diskGiB}
                oninput={() => (sizeEdited = true)}
              />
            </div>
          {/if}
        </div>
        {#if parsedResources.error}
          <p class="text-xs text-destructive">{parsedResources.error}</p>
        {:else}
          <p class="text-xs text-muted-foreground">{sizeHint}</p>
        {/if}
      </div>

      <div class="space-y-2">
        <Label>Image</Label>
        <div class="space-y-2 rounded-md border border-border p-3">
          <label
            class="flex items-start gap-2.5 text-sm"
            class:cursor-pointer={selectedAgent?.official_image}
            class:cursor-not-allowed={!selectedAgent?.official_image}
            class:opacity-60={!selectedAgent?.official_image}
          >
            <input
              type="radio"
              bind:group={imageStrategy}
              value="official"
              disabled={!selectedAgent?.official_image}
              class="mt-0.5 size-4 border-input disabled:cursor-not-allowed"
            />
            <div class="flex-1">
              <div class="font-medium">Default deepcove image</div>
              <div class="text-xs text-muted-foreground">
                {#if selectedAgent?.official_image}
                  Prebuilt and ready, the fastest boot. <code>{selectedAgent.official_image}</code>
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
                class="flex w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
              ></textarea>
              <p class="text-xs text-muted-foreground">
                Runs once on container start. Make it idempotent. Checking
                <code>command -v &lt;bin&gt;</code> before installing is the convention.
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
                class="flex w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-xs transition-colors focus:outline-none focus:ring-1 focus:ring-ring"
              ></textarea>
            </div>
          </div>
        {/if}
      </div>

      <div class="space-y-2">
        <div class="flex items-center justify-between">
          <Label>Folder</Label>
          <button
            type="button"
            role="switch"
            aria-checked={useFolder}
            onclick={toggleFolder}
            class="flex items-center gap-2 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
          >
            <span>Use a folder</span>
            <span
              class="relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors {useFolder
                ? 'bg-primary'
                : 'bg-input'}"
            >
              <span
                class="inline-block size-4 rounded-full bg-background shadow transition-transform {useFolder
                  ? 'translate-x-4'
                  : 'translate-x-0.5'}"
              ></span>
            </span>
          </button>
        </div>
        {#if !useFolder}
          <p class="text-xs text-muted-foreground">
            No folder. The agent starts in an empty <code>/workspace</code>. Turn on "Use a folder"
            to mount your code.
          </p>
        {:else}
          <p class="text-xs text-muted-foreground">
            The agent works in this folder (mounted at <code>/workspace</code>).
          </p>
          {#each mounts as m, i (i)}
            <div class="space-y-2 rounded-md border border-border p-2.5">
              <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
                <div class="min-w-0 space-y-1 sm:col-span-2">
                  <Label class="text-xs" for="mount-host-{i}">Host path</Label>
                  <HostPathField id="mount-host-{i}" bind:value={m.host_path} host={selectedHost} />
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

      {#if dockerLoaded && runtimeBlocked}
        <div
          class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-foreground"
        >
          {runtimeBlocked}
          {#if !noRuntimeUsable}
            Pick another runtime above to create a session now.
          {/if}
        </div>
      {:else if pulling}
        <!-- Live download progress. Determinate once the daemon reports
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

      <div class="flex items-center justify-end gap-2">
        <Button variant="ghost" onclick={() => (open = false)} disabled={submitting || pulling}>
          Cancel
        </Button>
        <Button
          onclick={submit}
          disabled={submitting ||
            pulling ||
            !name.trim() ||
            !!runtimeBlocked ||
            (imageStrategy === 'official' && !selectedAgent?.official_image) ||
            (imageStrategy === 'custom' && !customImage.trim()) ||
            !!parsedResources.error}
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
    </div>
  </DialogContent>
</Dialog>
