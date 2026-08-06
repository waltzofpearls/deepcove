// Maps API/network failures to human copy. The server tags errors it
// expects the UI to act on with a stable `code` (see
// internal/session/handlers.go); we turn those into a title + guidance,
// and fall back to the raw message for anything unrecognized.

import { ApiError } from './client';

export interface FriendlyError {
  title: string;
  guidance?: string;
  // raw is the underlying server/exception message, shown as muted
  // secondary detail for the curious / for bug reports.
  raw: string;
  // code passes through so callers can branch on specific cases (e.g.
  // offering a "Download image" action for image_not_found).
  code?: string;
}

export function friendlyError(e: unknown): FriendlyError {
  if (e instanceof ApiError) {
    switch (e.code) {
      case 'runtime_unavailable':
        return {
          title: "That runtime isn't available",
          // The server names which runtime and what to do ("Docker isn't running. Start it to use this
          // runtime."), which beats anything generic written here: the answer differs per runtime.
          guidance: e.message,
          raw: e.message,
          code: e.code,
        };
      case 'image_not_found':
        return {
          title: 'Agent image needs downloading',
          guidance: 'This image is downloaded once, then reused for every session.',
          raw: e.message,
          code: e.code,
        };
      case 'invalid_input':
        return { title: 'Check the form and try again', raw: e.message, code: e.code };
      case 'conflict':
        return { title: 'That conflicts with the current state', raw: e.message, code: e.code };
      case 'not_found':
        return { title: 'Not found', raw: e.message, code: e.code };
      default:
        return { title: 'Something went wrong', raw: e.message, code: e.code };
    }
  }
  return {
    title: 'Something went wrong',
    raw: e instanceof Error ? e.message : String(e),
  };
}
