<script lang="ts">
  import type { SessionHost } from '$lib/api/types';
  // The workspace VM's own settings: the folder it shares, where that folder appears inside the VM, and
  // how big the machine is. Used by the new-session dialog, the Nodes page and the setup wizard, so
  // the three never drift into asking for the same thing differently.
  import { Label } from '$lib/components/ui/label';
  import { Input } from '$lib/components/ui/input';
  import HostPathField from './HostPathField.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { looksLikePath } from '$lib/util/path';
  import VMSizePicker from './VMSizePicker.svelte';
  import { DEFAULT_VM_SIZE, workspaceVMSizeCards } from '$lib/util/vmSize';

  let {
    hostRoot = $bindable(''),
    guestRoot = $bindable('/workspace'),
    size = $bindable(DEFAULT_VM_SIZE),
    host,
  }: {
    hostRoot?: string;
    guestRoot?: string;
    size?: string;
    host?: SessionHost;
  } = $props();

  const sizeCards = $derived(workspaceVMSizeCards(preflight.runtimes?.workspace_vm_sizes ?? []));

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

<VMSizePicker
  title="Workspace VM size"
  cards={sizeCards}
  selected={size}
  onselect={(name) => (size = name)}
/>
