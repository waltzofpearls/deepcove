// Render ANSI bytes to HTML using an *unmounted* xterm.js Terminal +
// SerializeAddon. Same renderer the full view uses, so card previews
// and full-view xterm.js stay pixel-identical without us shipping a
// parallel ANSI parser server-side.
//
// Key trick: we never call term.open(), so no DOM/canvas/WebGL is
// created. The buffer state machine still runs on write(), and the
// addon serializes the buffer to HTML directly. xterm.js officially
// supports this path (the typings doc recommends "before Terminal.open
// is called" specifically). When _themeService is absent (unmounted),
// the addon falls back to DEFAULT_ANSI_COLORS — the Tango palette,
// which is also what xterm.js's full-view renders with our default
// theme. So colors match by default; no palette plumbing needed.
//
// fontSize / fontFamily are set on the Terminal so the serializer
// emits them in the outer wrapper div's inline style. That way the
// CSS in the consumer component never has to fight scoped-selector
// specificity to override them — what xterm.js writes is what renders.
//
// Each convert() call constructs and disposes a fresh Terminal.
// Reuse-with-resize was theoretically cheaper but xterm.js's resize
// reflows the buffer, and reset doesn't always wipe state cleanly
// after a resize — at terminal scale this was the suspect during
// the convertEol bug-hunt. Per-call construction is microseconds,
// and rules out a whole class of state-carryover bugs.

type ConvertOpts = {
  fontSize: number;
  fontFamily?: string;
};

// Font sizes used by the two consumers. Exported so consumers don't
// hard-code literals — keeps card vs full-view scale visible in one
// place if anyone wants to tune them.
export const CARD_FONT_SIZE = 10;
export const FULLVIEW_FONT_SIZE = 13;

type Converter = {
  convert: (ansi: string, opts: ConvertOpts) => Promise<string>;
  dispose: () => void;
};

const DEFAULT_FONT_FAMILY = 'ui-monospace, "JetBrains Mono", Menlo, monospace';

export async function createConverter(): Promise<Converter> {
  const [{ Terminal }, { SerializeAddon }] = await Promise.all([
    import('@xterm/xterm'),
    import('@xterm/addon-serialize'),
  ]);

  return {
    async convert(ansi, opts) {
      const { cols, rows } = measureAnsi(ansi);
      const fontFamily = opts.fontFamily ?? DEFAULT_FONT_FAMILY;
      // Strip trailing newlines before writing. capture-pane pads empty
      // rows up to pane_height with bare \n's. Each trailing \n past the
      // last content row scrolls the buffer (scrollback:0 means those
      // rows are discarded). Trim preserves the content as written.
      const trimmed = ansi.replace(/\n+$/, '');

      // Pull current theme bg/fg from CSS vars so light/dark switches
      // take effect on the next poll. The ANSI palette stays the
      // xterm.js default (Tango) — matching the full view.
      const style = getComputedStyle(document.documentElement);
      const v = (n: string, fb: string) => style.getPropertyValue(n).trim() || fb;
      const theme = {
        background: v('--term-bg', '#0b0d10'),
        foreground: v('--term-fg', '#e6e6e6'),
      };

      const term = new Terminal({
        cols,
        rows,
        allowProposedApi: true,
        scrollback: 0,
        fontSize: opts.fontSize,
        fontFamily,
        // capture-pane outputs bare \n between rows. xterm.js's
        // default LF behavior moves the cursor DOWN but keeps the
        // column — so each subsequent row would start at the column
        // where the previous row's content ended, producing a
        // right-shifted, fragmented layout. convertEol makes \n
        // behave like \r\n (cursor returns to col 0).
        convertEol: true,
        theme,
      });
      const serial = new SerializeAddon();
      term.loadAddon(serial);

      try {
        return await new Promise<string>((resolve) => {
          term.write(trimmed, () => {
            const html = serial.serializeAsHTML({ includeGlobalBackground: true });
            const a = '<!--StartFragment-->';
            const b = '<!--EndFragment-->';
            const i = html.indexOf(a);
            const j = html.indexOf(b);
            resolve(i === -1 || j === -1 ? html : html.slice(i + a.length, j));
          });
        });
      } finally {
        term.dispose();
      }
    },
    dispose() {
      // No shared state to release — each convert() owns its own
      // Terminal and disposes it on return. Kept for API symmetry
      // so callers can `converter?.dispose()` unconditionally.
    },
  };
}

// Derive the Terminal dimensions from the ANSI bytes themselves.
//
// Rows: index of the last line with any non-whitespace content,
// plus 1. Trailing blank rows from capture-pane padding are dropped.
//
// Cols: longest printable line, after stripping ANSI escapes. Going
// SMALLER than content would cause xterm.js to wrap inside its own
// buffer (one source row split into multiple buffer rows) — exactly
// the bug we're avoiding.
function measureAnsi(ansi: string): { cols: number; rows: number } {
  const stripped = ansi
    .replace(/\x1b\[[0-9;:?]*[ -/]*[@-~]/g, '')
    .replace(/\x1b\].*?(\x07|\x1b\\)/g, '');
  const lines = stripped.split('\n');
  let maxCols = 1;
  let lastNonEmpty = 0;
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    // \r returns to col 0; measure the longest segment between CRs.
    let lineMax = 0;
    for (const seg of line.split('\r')) {
      if (seg.length > lineMax) lineMax = seg.length;
    }
    if (lineMax > maxCols) maxCols = lineMax;
    if (line.trim() !== '') lastNonEmpty = i;
  }
  return { cols: maxCols, rows: lastNonEmpty + 1 };
}
