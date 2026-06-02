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
  import { friendlyError, type FriendlyError } from '$lib/api/errors';
  import { sessions } from '$lib/stores/sessions.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import type { Agent, MountRequest } from '$lib/api/types';
  import Copy from '@lucide/svelte/icons/copy';
  import Check from '@lucide/svelte/icons/check';
  import Plus from '@lucide/svelte/icons/plus';
  import X from '@lucide/svelte/icons/x';
  import Download from '@lucide/svelte/icons/download';

  let { open = $bindable(false) }: { open?: boolean } = $props();

  type FormMount = { host_path: string; container_path: string; read_only: boolean };
  type ImageStrategy = 'install' | 'official' | 'custom';

  let agents = $state<Agent[]>([]);
  let agentsError = $state<string | null>(null);
  let agent = $state('claude');
  let name = $state('');
  let mounts = $state<FormMount[]>([]);

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
      // Only emit overrides — keeps the CLI line short in the common case.
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

  function shellQuote(s: string): string {
    if (/^[A-Za-z0-9_@%+=:,./\-~]+$/.test(s)) return s;
    return `'${s.replace(/'/g, "'\\''")}'`;
  }

  function addMount() {
    mounts = [...mounts, { host_path: '', container_path: '/deepcove', read_only: false }];
  }

  function removeMount(i: number) {
    mounts = mounts.filter((_, idx) => idx !== i);
  }

  function resetForm() {
    name = '';
    mounts = [];
    submitError = null;
    submitting = false;
    pulling = false;
    pullPercent = -1;
    pullStatus = '';
    imagePresent = null;
    customImage = '';
    customSetup = '';
    // baseImage / setupScript / imageStrategy get re-seeded by the agent-change $effect.
  }

  function buildPayload() {
    const payload: {
      agent: string;
      name?: string;
      mounts: MountRequest[];
      image?: string;
      setup_script?: string;
    } = {
      agent,
      name: name.trim() || undefined,
      mounts: mounts
        // Drop incomplete rows — server would 400 anyway.
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
      // install — only emit overrides so registry updates flow into sessions on recreate.
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
  // show a "will download" hint before the user commits. Debounced — the
  // custom-image field changes per keystroke. Reads the image-strategy
  // inputs so Svelte re-runs this when any of them change.
  $effect(() => {
    const img = effectiveImage();
    const reachable = preflight.dockerReachable;
    imagePresent = null;
    if (!img || !reachable) return;
    let cancelled = false;
    const timer = setTimeout(async () => {
      try {
        const present = await api.imageExists(img);
        if (!cancelled) imagePresent = present;
      } catch {
        // Don't block create on a flaky check — leave it unknown; the
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
      await api.pullImageStream(effectiveImage(), (p) => {
        pullPercent = p.percent;
        pullStatus = p.status;
      });
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
  <DialogContent class="max-h-[90vh] w-[calc(100%-2rem)] max-w-3xl gap-0 p-0 sm:max-w-3xl">
    <DialogHeader class="border-b border-border px-6 py-4">
      <DialogTitle>New session</DialogTitle>
      <DialogDescription>
        Spin up a coding-agent sandbox on this node. Mount a host directory to give the
        agent access to your code.
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
          <Label for="session-name">Name (optional)</Label>
          <Input
            id="session-name"
            bind:value={name}
            placeholder="auto-generated if blank"
          />
        </div>
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
                  Prebuilt and ready — fastest boot. <code>{selectedAgent.official_image}</code>
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
                Start from a base image and install the agent on first boot
                (~15s for Claude).
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
                Runs once on container start. Make it idempotent — checking
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
          <Label>Mounts</Label>
          <Button size="sm" variant="ghost" onclick={addMount}>
            <Plus class="size-4" />
            Add mount
          </Button>
        </div>
        {#if mounts.length === 0}
          <p class="text-xs text-muted-foreground">
            No mounts. Add one to expose a host directory to the agent.
          </p>
        {/if}
        {#each mounts as m, i (i)}
          <div class="space-y-2 rounded-md border border-border p-2.5">
            <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
              <div class="space-y-1">
                <Label class="text-xs" for="mount-host-{i}">Host path</Label>
                <Input
                  id="mount-host-{i}"
                  bind:value={m.host_path}
                  placeholder="/Users/you/code/myrepo"
                />
              </div>
              <div class="space-y-1">
                <Label class="text-xs" for="mount-container-{i}">Container path</Label>
                <Input
                  id="mount-container-{i}"
                  bind:value={m.container_path}
                  placeholder="/deepcove"
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
                aria-label="Remove mount"
              >
                <X class="size-4" />
              </Button>
            </div>
          </div>
        {/each}
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

      {#if preflight.loaded && !preflight.dockerReachable}
        <div class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-foreground">
          {preflight.docker.message || "Docker isn't running."} Start it to create a session.
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
            This agent image isn't downloaded yet. It downloads once when you create
            the session, then is reused.
          </span>
        </div>
      {/if}

      <div class="flex items-center justify-end gap-2">
        <Button variant="ghost" onclick={() => (open = false)} disabled={submitting || pulling}>
          Cancel
        </Button>
        <Button
          onclick={submit}
          disabled={submitting
            || pulling
            || !preflight.dockerReachable
            || (imageStrategy === 'official' && !selectedAgent?.official_image)
            || (imageStrategy === 'custom' && !customImage.trim())}
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
        <summary class="flex cursor-pointer items-center justify-between rounded-md px-3 py-2 text-sm font-medium hover:bg-secondary/50">
          <span>Or run from CLI</span>
          <span class="text-xs text-muted-foreground group-open:hidden">show</span>
          <span class="hidden text-xs text-muted-foreground group-open:inline">hide</span>
        </summary>
        <div class="border-t border-border p-3">
          <div class="flex items-start gap-2 rounded-md border border-border bg-secondary p-2.5 font-mono text-xs">
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
            Run on the machine you want to host the session. The new session appears in
            the dashboard once it's running.
          </p>
        </div>
      </details>
    </div>
  </DialogContent>
</Dialog>
