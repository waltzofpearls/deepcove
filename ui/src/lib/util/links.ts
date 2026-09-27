// opensInNewTab reports whether a click on a link is the browser's to handle: a new tab or window.
export function opensInNewTab(e: MouseEvent): boolean {
  return e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0;
}
