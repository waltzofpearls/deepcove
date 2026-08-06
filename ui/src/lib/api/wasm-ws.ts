// A minimal WebSocket client implemented over the tailscale-web raw TCP stream
// (dialTCP), so the terminal's PTY WebSocket works in wasm mode against the
// node's *existing* WS endpoint, so the server is unchanged.
//
// We dial :80 (not :443): the WireGuard tunnel already encrypts everything, so
// plain HTTP avoids implementing TLS in the browser. Over that stream we do the
// RFC 6455 handshake, then frame: client→server frames are masked (required),
// server→client frames are parsed (incl. fragmentation + extended lengths) and
// surfaced through the DeepcoveSocket interface the terminal already uses. The
// forked `onClose` on the dialTCP connection tells us when the pipe dies.

import { ensureTailnet } from './wasm';
import type { DeepcoveSocket } from './transport';

const OP_CONT = 0x0;
const OP_TEXT = 0x1;
const OP_BIN = 0x2;
const OP_CLOSE = 0x8;
const OP_PING = 0x9;
const OP_PONG = 0xa;

interface Frame {
  fin: boolean;
  opcode: number;
  payload: Uint8Array;
}

// A complete, de-fragmented message (data frames merged across continuations;
// control frames passed through).
export interface WsMessage {
  opcode: number;
  payload: Uint8Array;
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  if (a.length === 0) return b;
  const out = new Uint8Array(a.length + b.length);
  out.set(a, 0);
  out.set(b, a.length);
  return out;
}

// encodeClientFrame builds one masked client→server frame. RFC 6455 requires
// clients to mask; the 4-byte key is random per frame.
export function encodeClientFrame(opcode: number, payload: Uint8Array): Uint8Array {
  const len = payload.length;
  const header: number[] = [0x80 | (opcode & 0x0f)]; // FIN + opcode
  if (len < 126) {
    header.push(0x80 | len); // MASK bit + length
  } else if (len < 0x10000) {
    header.push(0x80 | 126, (len >>> 8) & 0xff, len & 0xff);
  } else {
    header.push(0x80 | 127);
    for (let i = 7; i >= 0; i--) header.push(Math.floor(len / 2 ** (8 * i)) & 0xff);
  }
  const mask = new Uint8Array(4);
  crypto.getRandomValues(mask);
  const frame = new Uint8Array(header.length + 4 + len);
  frame.set(header, 0);
  frame.set(mask, header.length);
  const off = header.length + 4;
  for (let i = 0; i < len; i++) frame[off + i] = payload[i] ^ mask[i & 3];
  return frame;
}

// FrameDecoder turns an arbitrarily-chunked server byte stream into complete,
// de-fragmented messages. It tolerates partial frames across push() calls.
export class FrameDecoder {
  private buf: Uint8Array = new Uint8Array(0);
  private contOpcode = 0;
  private contParts: Uint8Array[] = [];

  push(chunk: Uint8Array): WsMessage[] {
    this.buf = concat(this.buf, chunk);
    const out: WsMessage[] = [];
    for (;;) {
      const f = this.parse();
      if (!f) break;
      if (f.opcode === OP_CONT) {
        this.contParts.push(f.payload);
        if (f.fin) {
          out.push({ opcode: this.contOpcode, payload: mergeAll(this.contParts) });
          this.contParts = [];
          this.contOpcode = 0;
        }
      } else if (f.opcode === OP_TEXT || f.opcode === OP_BIN) {
        if (f.fin) {
          out.push({ opcode: f.opcode, payload: f.payload });
        } else {
          this.contOpcode = f.opcode;
          this.contParts = [f.payload];
        }
      } else {
        // Control frame (close/ping/pong), never fragmented, may interleave.
        out.push({ opcode: f.opcode, payload: f.payload });
      }
    }
    return out;
  }

  private parse(): Frame | null {
    const b = this.buf;
    if (b.length < 2) return null;
    const fin = (b[0] & 0x80) !== 0;
    const opcode = b[0] & 0x0f;
    const masked = (b[1] & 0x80) !== 0;
    let len = b[1] & 0x7f;
    let off = 2;
    if (len === 126) {
      if (b.length < 4) return null;
      len = (b[2] << 8) | b[3];
      off = 4;
    } else if (len === 127) {
      if (b.length < 10) return null;
      len = 0;
      for (let i = 0; i < 8; i++) len = len * 256 + b[2 + i];
      off = 10;
    }
    let mask: Uint8Array | undefined;
    if (masked) {
      if (b.length < off + 4) return null;
      mask = b.subarray(off, off + 4);
      off += 4;
    }
    if (b.length < off + len) return null; // payload not fully arrived yet
    const payload = b.slice(off, off + len);
    if (mask) for (let i = 0; i < len; i++) payload[i] ^= mask[i & 3];
    this.buf = b.slice(off + len);
    return { fin, opcode, payload };
  }
}

function mergeAll(parts: Uint8Array[]): Uint8Array {
  let total = 0;
  for (const p of parts) total += p.length;
  const out = new Uint8Array(total);
  let o = 0;
  for (const p of parts) {
    out.set(p, o);
    o += p.length;
  }
  return out;
}

const READY = { CONNECTING: 0, OPEN: 1, CLOSING: 2, CLOSED: 3 };

// WasmWebSocket implements the DeepcoveSocket surface over a dialTCP stream.
// Returned synchronously in CONNECTING; the dial + handshake run in the
// background and fire onopen when ready, mirroring the native WebSocket.
class WasmWebSocket {
  binaryType: BinaryType = 'blob';
  readyState: number = READY.CONNECTING;
  onopen: ((ev: Event) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  onclose: ((ev: CloseEvent) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;

  private conn: Awaited<ReturnType<typeof dialNode>> | undefined;
  private decoder = new FrameDecoder();
  private handshakeBuf: Uint8Array = new Uint8Array(0);
  private handshakeDone = false;
  private pending: Uint8Array[] = [];
  private closed = false;

  constructor(host: string, path: string, subprotocol?: string) {
    this.start(host, path, subprotocol).catch((err) => this.fail(err));
  }

  private async start(host: string, path: string, subprotocol?: string) {
    const conn = await dialNode(host);
    if (this.closed) {
      conn.close();
      return;
    }
    this.conn = conn;
    conn.onClose(() => this.fireClose());
    conn.onData((d) => this.onData(d));
    conn.write(buildHandshake(host, path, subprotocol));
  }

  private onData(chunk: Uint8Array) {
    if (!this.handshakeDone) {
      this.handshakeBuf = concat(this.handshakeBuf, chunk);
      const end = indexOfDoubleCRLF(this.handshakeBuf);
      if (end < 0) return; // headers not complete yet
      const header = new TextDecoder().decode(this.handshakeBuf.subarray(0, end));
      const rest = this.handshakeBuf.slice(end + 4);
      this.handshakeBuf = new Uint8Array(0);
      if (!isSwitchingProtocols(header)) {
        this.fail(new Error('websocket handshake failed: ' + header.split('\r\n')[0]));
        return;
      }
      this.handshakeDone = true;
      this.readyState = READY.OPEN;
      this.onopen?.({} as Event);
      for (const w of this.pending) this.conn?.write(w);
      this.pending = [];
      if (rest.length) this.consume(rest);
      return;
    }
    this.consume(chunk);
  }

  private consume(chunk: Uint8Array) {
    for (const msg of this.decoder.push(chunk)) {
      switch (msg.opcode) {
        case OP_TEXT:
          this.onmessage?.({ data: new TextDecoder().decode(msg.payload) } as MessageEvent);
          break;
        case OP_BIN: {
          // Copy into a fresh ArrayBuffer (the decoder reuses its buffer, and it
          // sidesteps the SharedArrayBuffer-vs-ArrayBuffer typing).
          const ab = new ArrayBuffer(msg.payload.length);
          new Uint8Array(ab).set(msg.payload);
          const data = this.binaryType === 'blob' ? new Blob([ab]) : ab;
          this.onmessage?.({ data } as MessageEvent);
          break;
        }
        case OP_PING:
          this.conn?.write(encodeClientFrame(OP_PONG, msg.payload));
          break;
        case OP_CLOSE:
          this.fireClose();
          this.conn?.close();
          break;
        // OP_PONG: ignore
      }
    }
  }

  send(data: string | ArrayBufferView | ArrayBuffer) {
    const { opcode, bytes } = toFramePayload(data);
    const frame = encodeClientFrame(opcode, bytes);
    if (this.readyState !== READY.OPEN || !this.conn) {
      this.pending.push(frame); // flushed after the handshake
      return;
    }
    this.conn.write(frame);
  }

  close() {
    if (this.closed) return;
    this.readyState = READY.CLOSING;
    if (this.conn) {
      this.conn.write(encodeClientFrame(OP_CLOSE, new Uint8Array(0)));
      this.conn.close();
    }
    this.fireClose();
  }

  private fireClose() {
    if (this.closed) return;
    this.closed = true;
    this.readyState = READY.CLOSED;
    this.onclose?.({} as CloseEvent);
  }

  private fail(err: unknown) {
    this.onerror?.({ error: err } as unknown as Event);
    this.fireClose();
  }
}

async function dialNode(host: string) {
  const net = await ensureTailnet();
  return net.dialTCP(`${host}:80`);
}

function buildHandshake(host: string, path: string, subprotocol?: string): Uint8Array {
  const key = randomKey();
  const lines = [
    `GET ${path} HTTP/1.1`,
    `Host: ${host}`,
    'Upgrade: websocket',
    'Connection: Upgrade',
    `Sec-WebSocket-Key: ${key}`,
    'Sec-WebSocket-Version: 13',
  ];
  if (subprotocol) lines.push(`Sec-WebSocket-Protocol: ${subprotocol}`);
  return new TextEncoder().encode(lines.join('\r\n') + '\r\n\r\n');
}

function randomKey(): string {
  const b = new Uint8Array(16);
  crypto.getRandomValues(b);
  let s = '';
  for (const x of b) s += String.fromCharCode(x);
  return btoa(s);
}

// We accept any 101 response and don't verify Sec-WebSocket-Accept: the stream
// is already WireGuard-encrypted to an owner-gated peer we authenticated, so the
// accept-key check adds nothing here, and verifying it needs SHA-1 via
// crypto.subtle, which is unavailable on plain-http (insecure) origins.
function isSwitchingProtocols(header: string): boolean {
  return header.split(' ')[1] === '101';
}

function indexOfDoubleCRLF(b: Uint8Array): number {
  for (let i = 0; i + 3 < b.length; i++) {
    if (b[i] === 13 && b[i + 1] === 10 && b[i + 2] === 13 && b[i + 3] === 10) return i;
  }
  return -1;
}

function toFramePayload(data: string | ArrayBufferView | ArrayBuffer): {
  opcode: number;
  bytes: Uint8Array;
} {
  if (typeof data === 'string') {
    return { opcode: OP_TEXT, bytes: new TextEncoder().encode(data) };
  }
  if (data instanceof ArrayBuffer) {
    return { opcode: OP_BIN, bytes: new Uint8Array(data) };
  }
  return {
    opcode: OP_BIN,
    bytes: new Uint8Array(data.buffer, data.byteOffset, data.byteLength),
  };
}

// openWasmSocket dials the node and returns a WebSocket-like handle for `path`
// (a root-relative '/api/.../attach?...'), terminating at the node's existing
// WS endpoint over the tailnet.
export function openWasmSocket(host: string, path: string, subprotocol?: string): DeepcoveSocket {
  return new WasmWebSocket(host, path, subprotocol) as DeepcoveSocket;
}
