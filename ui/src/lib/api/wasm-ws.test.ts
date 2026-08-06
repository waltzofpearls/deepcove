import { describe, expect, it } from 'vitest';
import { FrameDecoder, encodeClientFrame } from './wasm-ws';

const OP_CONT = 0x0;
const OP_TEXT = 0x1;
const OP_BIN = 0x2;
const OP_PING = 0x9;

const enc = (s: string) => new TextEncoder().encode(s);
const dec = (b: Uint8Array) => new TextDecoder().decode(b);

function cat(...parts: Uint8Array[]): Uint8Array {
  let n = 0;
  for (const p of parts) n += p.length;
  const out = new Uint8Array(n);
  let o = 0;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}

// Build an UNMASKED server→client frame (servers don't mask).
function serverFrame(opcode: number, payload: Uint8Array, fin = true): Uint8Array {
  const len = payload.length;
  const header: number[] = [(fin ? 0x80 : 0) | opcode];
  if (len < 126) header.push(len);
  else if (len < 0x10000) header.push(126, (len >>> 8) & 0xff, len & 0xff);
  else {
    header.push(127);
    for (let i = 7; i >= 0; i--) header.push(Math.floor(len / 2 ** (8 * i)) & 0xff);
  }
  return cat(Uint8Array.from(header), payload);
}

describe('encodeClientFrame', () => {
  it('sets FIN + opcode and the MASK bit, and round-trips through the decoder', () => {
    const payload = enc('hello terminal');
    const frame = encodeClientFrame(OP_BIN, payload);
    expect(frame[0]).toBe(0x80 | OP_BIN); // FIN + binary
    expect(frame[1] & 0x80).toBe(0x80); // client frames MUST be masked
    // The decoder reads the mask bit and unmasks, recovering the payload.
    const msgs = new FrameDecoder().push(frame);
    expect(msgs).toHaveLength(1);
    expect(msgs[0].opcode).toBe(OP_BIN);
    expect(dec(msgs[0].payload)).toBe('hello terminal');
  });

  it('uses the 16-bit extended length for >125 byte payloads', () => {
    const payload = new Uint8Array(300).fill(7);
    const frame = encodeClientFrame(OP_BIN, payload);
    expect(frame[1] & 0x7f).toBe(126); // extended-16 marker
    const msgs = new FrameDecoder().push(frame);
    expect(msgs[0].payload.length).toBe(300);
    expect(Array.from(msgs[0].payload).every((b) => b === 7)).toBe(true);
  });
});

describe('FrameDecoder', () => {
  it('decodes a single text frame', () => {
    const msgs = new FrameDecoder().push(serverFrame(OP_TEXT, enc('{"type":"resize"}')));
    expect(msgs).toHaveLength(1);
    expect(msgs[0].opcode).toBe(OP_TEXT);
    expect(dec(msgs[0].payload)).toBe('{"type":"resize"}');
  });

  it('reassembles a fragmented message (TEXT fin=0 + CONT fin=1)', () => {
    const d = new FrameDecoder();
    expect(d.push(serverFrame(OP_TEXT, enc('hel'), false))).toHaveLength(0);
    const msgs = d.push(serverFrame(OP_CONT, enc('lo'), true));
    expect(msgs).toHaveLength(1);
    expect(msgs[0].opcode).toBe(OP_TEXT);
    expect(dec(msgs[0].payload)).toBe('hello');
  });

  it('tolerates a frame split across chunks (header + payload arrive separately)', () => {
    const frame = serverFrame(OP_BIN, enc('streamed'));
    const d = new FrameDecoder();
    expect(d.push(frame.slice(0, 1))).toHaveLength(0); // partial header
    expect(d.push(frame.slice(1, 4))).toHaveLength(0); // rest of header + some payload
    const msgs = d.push(frame.slice(4)); // remaining payload
    expect(msgs).toHaveLength(1);
    expect(dec(msgs[0].payload)).toBe('streamed');
  });

  it('parses two frames delivered in one chunk', () => {
    const chunk = cat(serverFrame(OP_BIN, enc('a')), serverFrame(OP_BIN, enc('b')));
    const msgs = new FrameDecoder().push(chunk);
    expect(msgs.map((m) => dec(m.payload))).toEqual(['a', 'b']);
  });

  it('surfaces control frames (ping) for the socket to answer', () => {
    const msgs = new FrameDecoder().push(serverFrame(OP_PING, enc('hb')));
    expect(msgs[0].opcode).toBe(OP_PING);
  });

  it('handles the 64-bit extended length path', () => {
    // Force the 127 marker with an 8-byte length even for a small payload.
    const payload = enc('sixtyfour');
    const header = Uint8Array.from([0x80 | OP_BIN, 127, 0, 0, 0, 0, 0, 0, 0, payload.length]);
    const msgs = new FrameDecoder().push(cat(header, payload));
    expect(dec(msgs[0].payload)).toBe('sixtyfour');
  });
});
