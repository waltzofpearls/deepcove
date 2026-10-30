import type { LLMProxy, LLMProxyHeaderEdit } from '$lib/api/types';

// The LLM proxy row edits the URL and the headers together and saves them with one button. Saved
// values never come back from the node, so a saved header with an empty value means keep it.

export interface HeaderRow {
  id: number;
  name: string;
  value: string;
  // saved rows have a locked name. To rename one, remove it and add another.
  saved: boolean;
}

export const newHostMessage =
  'The URL points at a new host. Enter this value again, or remove the header.';

export function rowsFromSaved(saved: Required<LLMProxy>): HeaderRow[] {
  return saved.headers.map((name, index) => ({
    id: index,
    name,
    value: '',
    saved: true,
  }));
}

// headerEdits is the full list a save sends. A header left out is removed.
export function headerEdits(rows: HeaderRow[]): LLMProxyHeaderEdit[] {
  return rows.map((row) => {
    if (row.saved && row.value === '') {
      return { name: row.name };
    }
    return {
      name: row.name,
      value: row.value,
    };
  });
}

export function changeCount(
  saved: Required<LLMProxy>,
  urlDraft: string,
  rows: HeaderRow[],
): number {
  const urlChanged = urlDraft.trim() !== saved.url ? 1 : 0;
  const removed = saved.headers.filter((name) => !rows.some((row) => row.name === name)).length;
  const edited = rows.filter((row) => !row.saved || row.value !== '').length;
  return urlChanged + removed + edited;
}

export function hasIncompleteNewRow(rows: HeaderRow[]): boolean {
  return rows.some((row) => !row.saved && (row.name.trim() === '' || row.value.trim() === ''));
}

// rowsNeedingValueAgain are saved headers whose value would go to a new host. The node refuses them too.
// This only says so before you click Save.
export function rowsNeedingValueAgain(
  saved: Required<LLMProxy>,
  urlDraft: string,
  rows: HeaderRow[],
): HeaderRow[] {
  if (saved.url === '' || sameHost(saved.url, urlDraft.trim())) {
    return [];
  }
  return rows.filter((row) => row.saved && row.value === '');
}

// sameHost compares host names only, the same way the node does. A URL that does not parse yet counts as
// the same, so the node's own error about the URL is the one you see.
function sameHost(savedURL: string, draftURL: string): boolean {
  if (draftURL === '') {
    return false;
  }
  try {
    return new URL(savedURL).hostname.toLowerCase() === new URL(draftURL).hostname.toLowerCase();
  } catch {
    return true;
  }
}

// sameHeaderName matches what you typed to the canonical name the node reports an error for.
export function sameHeaderName(typed: string, reported: string): boolean {
  return typed.trim().toLowerCase() === reported.toLowerCase();
}
