// Ghostty's default palette. xterm.js's own is harsh, and its red is hard to read in a diff. The
// terminal is dark in both app themes, so one palette covers both.
export const terminalColorPalette = {
  black: '#1d1f21',
  red: '#cc6666',
  green: '#b5bd68',
  yellow: '#f0c674',
  blue: '#81a2be',
  magenta: '#b294bb',
  cyan: '#8abeb7',
  white: '#c5c8c6',
  brightBlack: '#666666',
  brightRed: '#d54e53',
  brightGreen: '#b9ca4a',
  brightYellow: '#e7c547',
  brightBlue: '#7aa6da',
  brightMagenta: '#c397d8',
  brightCyan: '#70c0b1',
  brightWhite: '#eaeaea',
};

type ColorName = keyof typeof terminalColorPalette;

// xterm.js's own first 16 colors, the Tango palette.
const xtermDefaultColorPalette: Record<ColorName, string> = {
  black: '#2e3436',
  red: '#cc0000',
  green: '#4e9a06',
  yellow: '#c4a000',
  blue: '#3465a4',
  magenta: '#75507b',
  cyan: '#06989a',
  white: '#d3d7cf',
  brightBlack: '#555753',
  brightRed: '#ef2929',
  brightGreen: '#8ae234',
  brightYellow: '#fce94f',
  brightBlue: '#729fcf',
  brightMagenta: '#ad7fa8',
  brightCyan: '#34e2e2',
  brightWhite: '#eeeeec',
};

const replacements = new Map(
  (Object.keys(terminalColorPalette) as ColorName[]).map((name) => [
    xtermDefaultColorPalette[name],
    terminalColorPalette[name],
  ]),
);

// The preview serializer only reads a theme from a terminal open on the page, so it writes xterm.js's
// defaults. This swaps them for ours. An app that asked for one of those exact colors gets ours too.
export function withTerminalColorPalette(html: string): string {
  return html.replace(/#[0-9a-f]{6}\b/gi, (hex) => replacements.get(hex.toLowerCase()) ?? hex);
}
