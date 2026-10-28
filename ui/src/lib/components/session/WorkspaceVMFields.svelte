<script lang="ts">
  import type { SessionHost, WorkspaceVMSizeChoice } from '$lib/api/types';
  // The workspace VM's own settings: the folder it shares and how big the machine is. Used by the
  // new-session dialog, the Nodes page and the setup wizard, so the three never drift apart.
  import { Label } from '$lib/components/ui/label';
  import { Input } from '$lib/components/ui/input';
  import HostPathField from './HostPathField.svelte';
  import { preflight } from '$lib/stores/preflight.svelte';
  import { looksLikePath } from '$lib/util/path';
  import VMSizePicker from './VMSizePicker.svelte';
  import {
    DEFAULT_VM_SIZE,
    workspaceVMSizeCards,
    workspaceVMSizeOrDefault,
  } from '$lib/util/vmSize';

  // guestRoot is where the folder shows up in the VM. The field shows only when it is bound, since it
  // can be picked only when the VM is created.
  let {
    hostRoot = $bindable(''),
    guestRoot = $bindable(),
    size = $bindable(DEFAULT_VM_SIZE),
    host,
    nodeName = 'this node',
    sizes,
  }: {
    hostRoot?: string;
    guestRoot?: string;
    size?: string;
    host?: SessionHost;
    nodeName?: string;
    // The sizes the machine the VM goes on can have. This node's when not given and host is this node.
    sizes?: WorkspaceVMSizeChoice[];
  } = $props();

  const sizeChoices = $derived(
    sizes ?? (host ? undefined : preflight.runtimes?.workspace_vm_sizes),
  );
  const sizeCards = $derived(workspaceVMSizeCards(sizeChoices ?? []));
  // Sizes scale with the machine, so the default or a size picked elsewhere may not exist here.
  $effect(() => {
    const fitted = workspaceVMSizeOrDefault(sizeCards, size);
    if (fitted !== size) size = fitted;
  });

  const hostRootTyped = $derived(hostRoot.trim().length > 0);
  const guestRootTyped = $derived((guestRoot ?? '').trim().length > 0);
</script>

<div class="space-y-2">
  <Label for={guestRoot === undefined ? 'workspace-root' : undefined}>
    Folder this VM can see
    <span class="font-normal text-muted-foreground">(required)</span>
  </Label>
  {#if guestRoot === undefined}
    <HostPathField
      id="workspace-root"
      bind:value={hostRoot}
      placeholder="Pick a folder, like /Users/you/Dev"
      {host}
      startsEmpty
    />
  {:else}
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-5">
      <div class="min-w-0 space-y-1 sm:col-span-3">
        <Label class="text-xs font-normal text-muted-foreground" for="workspace-root">
          On {nodeName}
        </Label>
        <HostPathField
          id="workspace-root"
          bind:value={hostRoot}
          placeholder="Pick a folder, like /Users/you/Dev"
          {host}
          startsEmpty
        />
      </div>
      <div class="min-w-0 space-y-1 sm:col-span-2">
        <Label class="text-xs font-normal text-muted-foreground" for="workspace-guest-root">
          In the VM
        </Label>
        <Input id="workspace-guest-root" bind:value={guestRoot} placeholder="/workspace" />
      </div>
    </div>
  {/if}
  {#if hostRootTyped && !looksLikePath(hostRoot)}
    <p class="text-xs text-destructive">Enter a full path, starting with /.</p>
  {:else if guestRootTyped && !looksLikePath(guestRoot ?? '')}
    <p class="text-xs text-destructive">The folder in the VM needs a full path, starting with /.</p>
  {:else}
    <p class="text-xs text-muted-foreground">
      Every session on this VM can read and change everything in here.
    </p>
  {/if}
</div>

{#if sizeChoices === undefined}
  <!-- Still asking the machine what it can run. -->
{:else if sizeChoices.length === 0}
  <p class="text-sm text-muted-foreground">
    This machine has too little memory for a workspace VM. Use a session VM, or No sandbox.
  </p>
{:else}
  <VMSizePicker
    title="Workspace VM size"
    cards={sizeCards}
    selected={size}
    onselect={(name) => (size = name)}
  />
{/if}
