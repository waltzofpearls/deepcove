// Searching a session's terminal history. The search itself runs in tmux on the server; these decide what
// the browser asks for.

export type SearchDirection = 'older' | 'newer';

// isSearchShortcut is Cmd+F on a Mac and Ctrl+Shift+F elsewhere, since a shell reads Ctrl+F as "forward
// one character", the same reason copying is Ctrl+Shift+C.
export function isSearchShortcut(
  event: Pick<KeyboardEvent, 'key' | 'metaKey' | 'ctrlKey' | 'shiftKey'>,
): boolean {
  return event.key.toLowerCase() === 'f' && (event.metaKey || (event.ctrlKey && event.shiftKey));
}

// searchKeyAction is what a key in the search field does: Enter goes to the next older match, Shift+Enter to
// the next newer one, and Escape closes the search. Keys that finish an input method's composition, such as
// the Enter that picks a Japanese candidate, belong to the input method.
export function searchKeyAction(
  event: Pick<KeyboardEvent, 'key' | 'shiftKey' | 'isComposing'>,
): SearchDirection | 'close' | undefined {
  if (event.isComposing) return undefined;
  if (event.key === 'Escape') return 'close';
  if (event.key === 'Enter') return event.shiftKey ? 'newer' : 'older';
  return undefined;
}

// maxSearchMessageBytes keeps a search well under the 32 KiB the attach server reads in one frame. A longer
// one would close the terminal's connection, and no real search is that long.
const maxSearchMessageBytes = 4096;

// searchMessage is the control frame for a search, or undefined when it is too long to send.
export function searchMessage(
  text: string,
  direction: SearchDirection,
  searchId: number,
): string | undefined {
  const message = JSON.stringify({ type: 'search', text, direction, searchId });
  return new TextEncoder().encode(message).length <= maxSearchMessageBytes ? message : undefined;
}

// searchReply reads the server's answer to a search from a text frame, or undefined for any other frame. The
// problem is what the search bar shows, and a match has none.
export function searchReply(data: unknown): { searchId: number; problem?: string } | undefined {
  if (typeof data !== 'string') return undefined;
  try {
    const reply = JSON.parse(data);
    if (reply?.type !== 'search') return undefined;
    if (reply.isFailed) return { searchId: reply.searchId, problem: 'Search failed' };
    return { searchId: reply.searchId, problem: reply.isFound ? undefined : 'No matches' };
  } catch {
    return undefined;
  }
}
