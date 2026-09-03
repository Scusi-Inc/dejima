import { DejimaError } from "./errors.js";

/** One PTY session envelope on the wire (see openapi.yaml `SessionEnvelope`). */
export interface SessionEnvelope {
  type: "hello" | "data" | "resize" | "error" | "exit";
  b64?: string;
  rows?: number;
  cols?: number;
  attached?: Array<{ label: string; joined_at: string }>;
}

/**
 * A WebSocket as this SDK uses it. Both the browser `WebSocket`
 * (`addEventListener`) and the Node `ws` package (`on`) satisfy this; tests
 * inject a fake.
 */
export interface WebSocketLike {
  send(data: string): void;
  close(): void;
  addEventListener?(type: string, listener: (ev: any) => void): void;
  removeEventListener?(type: string, listener: (ev: any) => void): void;
  on?(event: string, listener: (...args: any[]) => void): void;
}

function base64Encode(bytes: Uint8Array): string {
  // Cross-runtime base64 (Buffer in Node, btoa in the browser).
  const g: any = globalThis as any;
  if (typeof g.Buffer !== "undefined") return g.Buffer.from(bytes).toString("base64");
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return g.btoa(bin);
}

function base64Decode(s: string): Uint8Array {
  const g: any = globalThis as any;
  if (typeof g.Buffer !== "undefined") return new Uint8Array(g.Buffer.from(s, "base64"));
  const bin = g.atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/**
 * An attached PTY session over the daemon's JSON-envelope WebSocket protocol.
 *
 * The wire is JSON envelopes with terminal bytes base64-encoded. {@link send}
 * and {@link recv} deal in raw `Uint8Array`; the framing is handled for you.
 * You can also drive it event-style with {@link onData}/{@link onClose}.
 */
export class Session {
  private ws: WebSocketLike;
  /** Buffered output chunks not yet pulled by recv(). */
  private buffer: Uint8Array[] = [];
  /** Pending recv() resolvers waiting for the next chunk. */
  private waiters: Array<(v: Uint8Array | null) => void> = [];
  private dataCbs: Array<(b: Uint8Array) => void> = [];
  private closeCbs: Array<() => void> = [];
  private errorCbs: Array<(e: DejimaError) => void> = [];
  private done = false;
  /** Clients attached to this session at connect time (from the `hello`). */
  attached: Array<{ label: string; joined_at: string }> = [];

  constructor(ws: WebSocketLike) {
    this.ws = ws;
    this.wire("message", (ev: any) => this.onMessage(typeof ev === "string" ? ev : ev?.data));
    this.wire("close", () => this.finish());
    this.wire("error", () => this.finish());
  }

  private wire(event: string, fn: (ev: any) => void): void {
    if (typeof this.ws.on === "function") this.ws.on(event, fn);
    else if (typeof this.ws.addEventListener === "function") this.ws.addEventListener(event, fn);
  }

  private onMessage(raw: any): void {
    if (raw == null) return;
    const text = typeof raw === "string" ? raw : raw.toString("utf-8");
    let env: SessionEnvelope;
    try {
      env = JSON.parse(text);
    } catch {
      return;
    }
    switch (env.type) {
      case "hello":
        this.attached = env.attached ?? [];
        break;
      case "data": {
        const bytes = base64Decode(env.b64 ?? "");
        for (const cb of this.dataCbs) cb(bytes);
        const w = this.waiters.shift();
        if (w) w(bytes);
        else this.buffer.push(bytes);
        break;
      }
      case "error": {
        // The daemon puts the plaintext error in `b64` for error envelopes.
        const err = new DejimaError(0, env.b64 ?? "session error");
        for (const cb of this.errorCbs) cb(err);
        this.finish();
        break;
      }
      case "exit":
        // The bridged terminal itself ended (a detach, an `exit`, a tmux that
        // died on start) — end the session rather than waiting for the socket.
        // Distinct from a transport drop on purpose: a caller that treats it as
        // one reconnects forever, respawning a shell nobody can escape.
        this.finish();
        break;
    }
  }

  private finish(): void {
    if (this.done) return;
    this.done = true;
    for (const cb of this.closeCbs) cb();
    for (const w of this.waiters.splice(0)) w(null);
  }

  /** Send raw bytes to the PTY (keystrokes). */
  send(data: Uint8Array): void {
    this.ws.send(JSON.stringify({ type: "data", b64: base64Encode(data) }));
  }

  /** Send a UTF-8 string to the PTY. */
  sendText(text: string): void {
    this.send(new TextEncoder().encode(text));
  }

  /** Resize the PTY. */
  resize(rows: number, cols: number): void {
    this.ws.send(JSON.stringify({ type: "resize", rows, cols }));
  }

  /**
   * Pull the next chunk of PTY output as raw bytes, or `null` once the session
   * has closed. Mirrors the Python client's blocking `recv()`.
   */
  recv(): Promise<Uint8Array | null> {
    const queued = this.buffer.shift();
    if (queued) return Promise.resolve(queued);
    if (this.done) return Promise.resolve(null);
    return new Promise((resolve) => this.waiters.push(resolve));
  }

  /** Register a callback for each output chunk. */
  onData(cb: (bytes: Uint8Array) => void): void {
    this.dataCbs.push(cb);
  }

  /** Register a callback for when the session closes. */
  onClose(cb: () => void): void {
    this.closeCbs.push(cb);
  }

  /** Register a callback for a server `error` envelope. */
  onError(cb: (e: DejimaError) => void): void {
    this.errorCbs.push(cb);
  }

  /** Close the underlying socket. */
  close(): void {
    try {
      this.ws.close();
    } catch {
      /* best effort */
    }
    this.finish();
  }
}
