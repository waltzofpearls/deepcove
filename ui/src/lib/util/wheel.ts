// Turns wheel events into a count of scroll steps to send to tmux.
//
// This exists because a mouse and a trackpad describe the same gesture completely differently. A wheel
// notch arrives as one large event; a trackpad swipe arrives as a stream of small pixel deltas. Sending
// one scroll key per event would make the mouse feel sluggish and the trackpad feel like it teleports, so
// both are normalized to pixels and accumulated until they add up to a notch.

// pxPerStep is roughly one wheel notch. Each step becomes one tmux scroll key.
const pxPerStep = 40;

// Browsers report deltaY in one of three units (WheelEvent.deltaMode). Only pixels are directly usable,
// so the other two are converted with conventional approximations.
const linePx = 16; // deltaMode 1: one text line
const pagePx = 400; // deltaMode 2: one viewport page

export interface WheelResult {
  // steps is negative to scroll UP (back through history) and positive to scroll DOWN, matching the sign
  // of deltaY. Zero means the gesture has not yet added up to a full step.
  steps: number;
  // carry is the leftover pixels to pass into the next call, so a slow trackpad drag still scrolls
  // instead of rounding away to nothing on every event.
  carry: number;
}

export function wheelSteps(deltaY: number, deltaMode: number, carry: number): WheelResult {
  const px = carry + toPixels(deltaY, deltaMode);
  // `|| 0` normalizes the -0 that Math.trunc returns for a small upward delta. It compares equal to 0 so
  // nothing misbehaves, but handing a caller negative zero for "no movement" invites a confusing bug.
  const steps = Math.trunc(px / pxPerStep) || 0;
  return { steps, carry: px - steps * pxPerStep };
}

function toPixels(deltaY: number, deltaMode: number): number {
  switch (deltaMode) {
    case 1:
      return deltaY * linePx;
    case 2:
      return deltaY * pagePx;
    default:
      return deltaY;
  }
}
