<script lang="ts">
  import type { SessionHost } from '$lib/api/types';
  // The workspace VM's own settings: the folder it shares, where that folder appears inside the VM, and
  // how big the machine is. Used by the new-session dialog, the Runtimes page and the setup wizard, so
  // the three never drift into asking for the same thing differently.
  import { Label } from '$lib/components/ui/label';
  import { Input } from '$lib/components/ui/input';
  import HostPathField from './HostPathField.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { looksLikePath } from '$lib/util/path';
  import {
    emptyResourceFields,
    fieldsFromDefaults,
    type ResourceFields,
  } from '$lib/util/resources';

  let {
    hostRoot = $bindable(''),
    guestRoot = $bindable('/workspace'),
    fields = $bindable<ResourceFields>({ ...emptyResourceFields }),
    creating = true,
    host,
  }: {
    hostRoot?: string;
    guestRoot?: string;
    fields?: ResourceFields;
    host?: SessionHost;
    // creating fills the form from the host's size. Editing keeps what the machine was saved with.
    creating?: boolean;
  } = $props();

  // Seeded from the host's own size, so the numbers are visible and editable rather than hidden behind
  // a placeholder. Keyed as a string because the preflight poll hands back a fresh object every tick,
  // and an effect depending on the reference would re-seed the form underneath the user.
  const defaultsKey = $derived(JSON.stringify(preflight.workspaceDefaults));
  let sizeEdited = $state(false);
  $effect(() => {
    const key = defaultsKey;
    if (!creating || sizeEdited || key === 'null') return;
    fields = fieldsFromDefaults(JSON.parse(key));
  });

  const hostRootTyped = $derived(hostRoot.trim().length > 0);
</script>

<div class="space-y-2">
  <Label for="workspace-root">Folder to share</Label>
  <HostPathField id="workspace-root" bind:value={hostRoot} placeholder="/Users/you/Dev" {host} />
  {#if hostRootTyped && !looksLikePath(hostRoot)}
    <p class="text-xs text-destructive">Enter a full path, starting with /.</p>
  {:else}
    <p class="text-xs text-muted-foreground">
      Every session on this VM works in its own folder inside this one.
    </p>
  {/if}
</div>

<div class="space-y-2">
  <Label for="workspace-guest-root">Mounted in the VM at</Label>
  <Input id="workspace-guest-root" bind:value={guestRoot} placeholder="/workspace" />
  <p class="text-xs text-muted-foreground">
    Where that folder appears inside the VM, and where each session's folder lives.
  </p>
</div>

<div class="space-y-2">
  <Label>Size</Label>
  <div class="grid grid-cols-3 gap-3">
    <div class="space-y-1.5">
      <Label for="workspace-cpu" class="text-xs font-normal text-muted-foreground">CPU cores</Label>
      <Input
        id="workspace-cpu"
        type="number"
        min="1"
        bind:value={fields.cpu}
        oninput={() => (sizeEdited = true)}
      />
    </div>
    <div class="space-y-1.5">
      <Label for="workspace-memory" class="text-xs font-normal text-muted-foreground">
        Memory (GiB)
      </Label>
      <Input
        id="workspace-memory"
        type="number"
        min="1"
        bind:value={fields.memoryGiB}
        oninput={() => (sizeEdited = true)}
      />
    </div>
    <div class="space-y-1.5">
      <Label for="workspace-disk" class="text-xs font-normal text-muted-foreground">
        Disk (GiB)
      </Label>
      <Input
        id="workspace-disk"
        type="number"
        min="1"
        step="1"
        bind:value={fields.diskGiB}
        oninput={() => (sizeEdited = true)}
      />
    </div>
  </div>
  <p class="text-xs text-muted-foreground">
    Sized for this machine, and shared by every session on the VM. Disk is sparse, so it costs what
    gets written.
  </p>
</div>
