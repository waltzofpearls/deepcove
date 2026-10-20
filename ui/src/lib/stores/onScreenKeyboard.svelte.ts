// Whether a phone or tablet's on-screen keyboard is up, and how much of the page is left above it.
//
// Focus cannot tell: iOS keeps the terminal focused when its own hide key closes the keyboard. The visible
// part of the page shrinking can, since the browser keeps the page itself full height.

import { untrack } from 'svelte';
import { keyboardShown, touchPrimaryQuery } from '$lib/util/mobileKeyBar';

let shown = $state(false);
let visibleHeight = $state(0);

// start follows the keyboard until the returned function is called. Off touch devices it stays closed.
function start(): () => void {
  const viewport = window.visualViewport;
  if (!viewport) return () => {};
  const touch = matchMedia(touchPrimaryQuery);
  const onResize = () => {
    if (!touch.matches) {
      shown = false;
      return;
    }
    // A pinch zoom shrinks the visible part too, and that is not a keyboard.
    if (Math.abs(viewport.scale - 1) > 0.01) return;
    const wasShown = shown;
    shown = keyboardShown(window.innerHeight, viewport.height);
    visibleHeight = viewport.height;
    // iOS also scrolls the page to show the focused field. The app is sized to fit, so undo that, but only
    // as the keyboard comes or goes: a page that scrolls, like setup, must keep its place.
    if (shown || wasShown) window.scrollTo(0, 0);
  };
  // Untracked: start runs inside the layout's effect, which must not restart when the keyboard comes or goes.
  untrack(onResize);
  viewport.addEventListener('resize', onResize);
  touch.addEventListener('change', onResize);
  return () => {
    viewport.removeEventListener('resize', onResize);
    touch.removeEventListener('change', onResize);
    shown = false;
  };
}

export const onScreenKeyboard = {
  get shown(): boolean {
    return shown;
  },
  get visibleHeight(): number {
    return visibleHeight;
  },
  start,
};
