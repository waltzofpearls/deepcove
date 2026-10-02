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

// showsRuntimePicker reports whether the create form asks which runtime to use. It asks whenever there
// is a choice, and always when No sandbox is on offer, so a session never ends up with no isolation
// without the user seeing it.
export function showsRuntimePicker(options: string[]): boolean {
  return options.length > 1 || options.includes(RUNTIME_SANDBOXLESS);
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

// runtimeCapsDisk reports whether a runtime actually applies a disk cap. Only the VM runtime does: its
// writable layer is a tmpfs that would otherwise be free to eat the guest's RAM. Anywhere else the
// input would promise something we do not deliver.
export function runtimeCapsDisk(name: string): boolean {
  return bootsVMs(name);
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
