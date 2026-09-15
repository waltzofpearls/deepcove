import type { RuntimesInfo } from '$lib/api/types';

// Presentation for sandbox runtimes. The wire values are the backend's runtime names
// (internal/session.Runtime*), kept short and stable; these turn them into something worth reading in
// the UI. An unknown name falls through to itself rather than being hidden, so a node offering a runtime
// this build has never heard of still renders sensibly.

export const RUNTIME_DOCKER = 'docker';
export const RUNTIME_APPLEVF = 'applevf';
export const RUNTIME_CLOUDHV = 'cloudhv';

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
      return 'Part of its setup is missing.';
    case 'no_host_binaries':
      return 'The programs it runs virtual machines with are not downloaded yet.';
    default:
      return 'It needs to be set up before it can run sessions.';
  }
}

// bootsVMs reports whether a runtime gives each machine its own kernel. Mirrors runtimeBootsVMs on the
// server. Workspace and session VMs exist only on these; Docker has no such concept.
export function bootsVMs(name: string): boolean {
  return VM_RUNTIMES.includes(name);
}

// runtimeLabel names the runtime the way it is talked about. "Virtual machine" describes the isolation
// boundary; Docker is known by its name, so "Container (Docker)" only added a word.
export function runtimeLabel(name: string): string {
  switch (name) {
    case RUNTIME_DOCKER:
      return 'Docker';
    case RUNTIME_APPLEVF:
    case RUNTIME_CLOUDHV:
      return 'Virtual machine';
    default:
      return name;
  }
}

// runtimeHint explains the tradeoff, since the choice is not obvious from the name alone.
export function runtimeHint(name: string): string {
  switch (name) {
    case RUNTIME_DOCKER:
      return 'Shares the host kernel. Starts fastest, and keeps your agent login between sessions.';
    case RUNTIME_APPLEVF:
    case RUNTIME_CLOUDHV:
      return 'Its own Linux kernel, so the session is fully isolated from your machine. Slower to start.';
    default:
      return '';
  }
}

// runtimeCapsDisk reports whether a runtime actually applies a disk cap. Only the VM runtime does: its
// writable layer is a tmpfs that would otherwise be free to eat the guest's RAM. Docker's own
// storage driver quotas need backing-filesystem support we do not require, so it ignores the field, and
// offering the input there would promise something we do not deliver.
export function runtimeCapsDisk(name: string): boolean {
  return bootsVMs(name);
}

// runtimeSummary is one line saying what a machine can host, for a list with no room for the detail.
export function runtimeSummary(info: RuntimesInfo | null | undefined): string {
  if (!info) return '';
  const parts = (info.available ?? []).map((name) =>
    info.unavailable?.[name] ? `${runtimeLabel(name)} unreachable` : runtimeLabel(name),
  );
  for (const name of Object.keys(info.missing ?? {})) {
    parts.push(`${runtimeLabel(name)} needs setup`);
  }
  return parts.join(' \u00b7 ');
}

// runtimeNeedsSetup reports whether this machine is waiting for something to be installed.
export function runtimeNeedsSetup(info: RuntimesInfo | null | undefined): boolean {
  return Object.keys(info?.missing ?? {}).length > 0;
}

// runtimeShortLabel is the tag-sized name, where the full label would not fit next to the agent tag.
// "VM" over "Virtual machine" because a tag is scanned, not read.
export function runtimeShortLabel(name: string): string {
  switch (name) {
    case RUNTIME_DOCKER:
      return 'Docker';
    case RUNTIME_APPLEVF:
    case RUNTIME_CLOUDHV:
      return 'VM';
    default:
      return name;
  }
}
