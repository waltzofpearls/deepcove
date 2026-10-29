import type { RuntimesInfo } from '$lib/api/types';

// Presentation for sandbox runtimes. The wire values are the backend's runtime names
// (internal/session.Runtime*), kept short and stable; these turn them into something worth reading in
// the UI. An unknown name falls through to itself rather than being hidden, so a node offering a runtime
// this build has never heard of still renders sensibly.

export const RUNTIME_APPLEVF = 'applevf';
export const RUNTIME_CLOUDHV = 'cloudhv';
export const RUNTIME_SANDBOXLESS = 'sandboxless';

// One VM runtime name per hypervisor, mirroring vmRuntimes on the server. A node offers at most one of
// them, so these never appear side by side in a picker.
const VM_RUNTIMES = [RUNTIME_APPLEVF, RUNTIME_CLOUDHV];

// usableRuntimes is what can host a session right now. Being registered says the driver exists;
// `unavailable` says whether it can be used at the moment, which is the half that changes.
export function usableRuntimes(info: RuntimesInfo | null | undefined): string[] {
  if (!info) return [];
  const down = info.unavailable ?? {};
  return info.available.filter((name) => !down[name]);
}

// runtimeMissingReason says what a runtime still needs, in the user's terms. The server sends a stable
// code so this wording can change without a server change. An unknown code falls back to something true
// but vague, since a newer node may report a reason this build has never heard of.
export function runtimeMissingReason(code: string): string {
  switch (code) {
    case 'no_kernel':
      return 'The Linux kernel it boots sessions with is not downloaded yet.';
    case 'no_guest_init':
      return 'Part of deepcove is missing. Reinstall deepcove to fix it.';
    case 'no_host_binaries':
      return 'The programs it runs virtual machines with are not downloaded yet.';
    case 'no_kvm':
      return "KVM is missing, or this user can't use it. If /dev/kvm exists, add this user to the kvm group, then reboot.";
    case 'start_failed':
      return 'It could not start. The reason is in deepcove\'s output, or run "deepcove service status" if it runs as a service.';
    case 'no_tmux':
      return 'tmux is not installed, and deepcove cannot install it for you. Install it, then restart deepcove.';
    default:
      return 'It needs to be set up before it can run sessions.';
  }
}

// missingReasons says why each missing runtime is missing, leaving No sandbox out so it is never pitched.
export function missingReasons(info: RuntimesInfo): string[] {
  return Object.entries(info.missing ?? {})
    .filter(([name]) => name !== RUNTIME_SANDBOXLESS)
    .map(([, code]) => runtimeMissingReason(code));
}

// runtimeSetUpByDeepcove reports whether deepcove can install what a runtime is missing, so a Set up
// button only appears where pressing it can work. The VM codes here must match vmInstallerOffered on the
// server; no_tmux is the extra one.
export function runtimeSetUpByDeepcove(code: string): boolean {
  return !['no_tmux', 'no_kvm', 'no_guest_init', 'start_failed'].includes(code);
}

// runtimeMissingShort is a missing reason sized for a one line summary: what to do when deepcove can do
// it, or the blocker when it cannot.
export function runtimeMissingShort(code: string): string {
  switch (code) {
    case 'no_guest_init':
      return 'reinstall deepcove';
    case 'no_kvm':
      return 'no KVM';
    case 'start_failed':
      return 'failed to start';
    case 'no_tmux':
      return 'no tmux';
    default:
      return 'needs setup';
  }
}

// runtimeSetupOffered reports whether something this machine is missing can be set up from deepcove, so
// a Set up pointer only appears where it leads to a button.
export function runtimeSetupOffered(info: RuntimesInfo | null | undefined): boolean {
  return Object.values(info?.missing ?? {}).some(runtimeSetUpByDeepcove);
}

// bootsVMs reports whether a runtime gives each machine its own kernel. Mirrors runtimeBootsVMs on the
// server. Workspace and session VMs exist only on these.
export function bootsVMs(name: string): boolean {
  return VM_RUNTIMES.includes(name);
}

// sharesHostFolders reports whether a runtime can share a folder with the session. A runtime with no
// guest has nothing to share it into: the agent reads the machine's own folders.
export function sharesHostFolders(name: string): boolean {
  return name !== RUNTIME_SANDBOXLESS;
}

// RuntimePlace is where a session runs, one per tile in the New session dialog's runtime step.
export type RuntimePlace = 'session_vm' | 'workspace_vm' | 'no_sandbox';

// runtimePlaceLabel names a place the way the New session dialog's tiles do.
export function runtimePlaceLabel(place: RuntimePlace): string {
  switch (place) {
    case 'session_vm':
      return 'Its own VM';
    case 'workspace_vm':
      return 'Workspace VM';
    case 'no_sandbox':
      return 'No sandbox';
  }
}

// RuntimeTile is one runtime choice the New session dialog offers.
export interface RuntimeTile {
  place: RuntimePlace;
  // runtime is the name the session is created with.
  runtime: string;
  isDefault: boolean;
  // needsSetup means the VM runtime is missing, and deepcove can download it from the dialog.
  needsSetup: boolean;
  // blocked says why the tile can't be picked, or is empty when it can.
  blocked: string;
}

// runtimeTiles lists what a node can run sessions on. Both VM tiles use the node's one VM runtime. A tile
// that can't run yet is still listed with the reason, so a choice never disappears without a word.
export function runtimeTiles(info: RuntimesInfo): RuntimeTile[] {
  const tiles: RuntimeTile[] = [];
  const missing = info.missing ?? {};
  const vmRuntime = info.available.find(bootsVMs);
  const missingVM = Object.keys(missing).find(bootsVMs);
  if (vmRuntime || missingVM) {
    const runtime = vmRuntime ?? missingVM ?? '';
    const needsSetup = !vmRuntime && runtimeSetUpByDeepcove(missing[runtime]);
    let blocked = info.unavailable?.[runtime] ?? '';
    if (!vmRuntime && !needsSetup) blocked = runtimeMissingReason(missing[runtime]);
    // The node names no default while its VM still needs the download, but the VM is still the pick.
    const vmIsDefault = bootsVMs(info.default) || (needsSetup && !info.default);
    tiles.push(
      { place: 'session_vm', runtime, isDefault: vmIsDefault, needsSetup, blocked },
      { place: 'workspace_vm', runtime, isDefault: false, needsSetup, blocked },
    );
  }
  if (info.available.includes(RUNTIME_SANDBOXLESS)) {
    tiles.push({
      place: 'no_sandbox',
      runtime: RUNTIME_SANDBOXLESS,
      isDefault: info.default === RUNTIME_SANDBOXLESS,
      needsSetup: false,
      blocked: info.unavailable?.[RUNTIME_SANDBOXLESS] ?? '',
    });
  } else if (missing[RUNTIME_SANDBOXLESS]) {
    tiles.push({
      place: 'no_sandbox',
      runtime: RUNTIME_SANDBOXLESS,
      isDefault: false,
      needsSetup: false,
      blocked: runtimeMissingReason(missing[RUNTIME_SANDBOXLESS]),
    });
  }
  return tiles;
}

// runtimeLabel names the runtime the way it is talked about. "Virtual machine" describes the isolation
// boundary.
export function runtimeLabel(name: string): string {
  switch (name) {
    case RUNTIME_APPLEVF:
    case RUNTIME_CLOUDHV:
      return 'Virtual machine';
    case RUNTIME_SANDBOXLESS:
      return 'No sandbox';
    default:
      return name;
  }
}

// runtimeHint explains the tradeoff, since the choice is not obvious from the name alone.
export function runtimeHint(name: string): string {
  switch (name) {
    case RUNTIME_APPLEVF:
    case RUNTIME_CLOUDHV:
      return 'Its own Linux kernel, so the session is fully isolated from your machine. Slower to start.';
    case RUNTIME_SANDBOXLESS:
      return "Runs directly on the host machine, with that machine's files, network and logins. Nothing is isolated.";
    default:
      return '';
  }
}

// runtimeSummary is one line saying what a machine can host, for a list with no room for the detail.
export function runtimeSummary(info: RuntimesInfo | null | undefined): string {
  if (!info) return '';
  const parts = (info.available ?? []).map((name) =>
    info.unavailable?.[name] ? `${runtimeLabel(name)} (not responding)` : runtimeLabel(name),
  );
  for (const [name, code] of Object.entries(info.missing ?? {})) {
    parts.push(`${runtimeLabel(name)} (${runtimeMissingShort(code)})`);
  }
  // Said out loud, or the row reads as a healthy machine that simply has no sessions.
  return parts.length > 0 ? parts.join(' \u00b7 ') : 'Nothing to run sessions on';
}
