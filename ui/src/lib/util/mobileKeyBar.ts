// The mobile key bar under the terminal on phones and tablets, for the keys an on-screen keyboard lacks.

export type MobileKey = 'escape' | 'tab' | 'shiftTab' | 'up' | 'down' | 'right' | 'left' | 'enter';

// Ctrl on the mobile key bar is sticky: one tap holds it for the next key, a second tap locks it on.
export type CtrlState = 'off' | 'once' | 'locked';

// Phones and tablets, but not a touch laptop, which also has a mouse or trackpad.
export const touchPrimaryQuery = '(hover: none) and (pointer: coarse)';

// An on-screen keyboard is far taller than this. The shortcut bar an iPad shows with a hardware keyboard
// attached is not, and leaves the mobile key bar's keyboard button saying the keyboard is closed.
const keyboardMinHeight = 150;

// keyboardShown says whether an on-screen keyboard covers the page: the page keeps its full height while
// the part left visible above the keyboard shrinks.
export function keyboardShown(pageHeight: number, visibleHeight: number): boolean {
  return pageHeight - visibleHeight > keyboardMinHeight;
}

const arrowFinal = { up: 'A', down: 'B', right: 'C', left: 'D' } as const;

const plainKeys = { escape: '\x1b', tab: '\t', shiftTab: '\x1b[Z', enter: '\r' } as const;

// mobileKeySequence is what a tap sends to the terminal. Arrows follow the terminal's cursor key mode,
// which full screen programs switch on, so a menu gets the codes it listens for.
export function mobileKeySequence(
  key: MobileKey,
  { applicationCursorKeys, ctrl }: { applicationCursorKeys: boolean; ctrl: boolean },
): string {
  if (key in arrowFinal) {
    const final = arrowFinal[key as keyof typeof arrowFinal];
    if (ctrl) return `\x1b[1;5${final}`;
    return applicationCursorKeys ? `\x1bO${final}` : `\x1b[${final}`;
  }
  return plainKeys[key as keyof typeof plainKeys];
}

export function nextCtrlState(state: CtrlState): CtrlState {
  if (state === 'off') return 'once';
  if (state === 'once') return 'locked';
  return 'off';
}

// withCtrl turns one typed character into its control code, as a hardware Ctrl would: Ctrl+C sends 0x03.
// Anything longer, like a paste or a predicted word, passes unchanged.
export function withCtrl(data: string): string {
  if (data.length !== 1) return data;
  if (data === ' ') return '\x00';
  if (data === '?') return '\x7f';
  // Only a to z: 'ß' upper cases to 'SS' and would send Ctrl+S, which freezes the terminal.
  const letter = /^[a-z]$/.test(data) ? data.toUpperCase() : data;
  const code = letter.charCodeAt(0);
  // @, A to Z, [, \, ], ^ and _ map to 0x00 to 0x1f.
  if (code >= 0x40 && code <= 0x5f) return String.fromCharCode(code - 0x40);
  return data;
}
