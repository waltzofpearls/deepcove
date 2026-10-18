import type { SessionResources, SharedFolder, VMSize } from '$lib/api/types';
import { formatRunningSize, runsAtSize } from './vmSize';

// A running VM keeps the settings it booted with. These report a saved setting it only picks up when it
// restarts.

// sizeAwaitingRestart reports whether a running VM still runs another size than the one saved for it.
export function sizeAwaitingRestart(
  running: SessionResources | undefined,
  saved: SessionResources | undefined,
  sizes: VMSize[] | undefined,
): boolean {
  if (!running || !saved) return false;
  const savedSize = sizes?.find((size) => size.name === saved.size);
  if (savedSize) return !runsAtSize(running, savedSize);
  const savedText = formatRunningSize(saved);
  return savedText !== '' && savedText !== formatRunningSize(running);
}

// folderAwaitingRestart reports whether a running workspace VM still shares another folder than the one
// saved for it.
export function folderAwaitingRestart(
  runningSharedFolders: SharedFolder[] | undefined,
  hostRoot: string,
  guestRoot: string,
): boolean {
  const shared = runningSharedFolders?.[0];
  if (!shared) return false;
  return shared.host_path !== hostRoot || shared.guest_path !== guestRoot;
}
