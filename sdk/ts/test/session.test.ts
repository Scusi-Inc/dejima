import { test } from "node:test";
import assert from "node:assert/strict";
import { Session, DejimaError } from "../src/index.js";

/** A fake WebSocketLike using the `on(event, …)` (Node `ws`) style. */
class FakeWS {
  sent: string[] = [];
  closed = false;
  private handlers: Record<string, Array<(ev: any) => void>> = {};

  on(event: string, fn: (ev: any) => void) {
    (this.handlers[event] ??= []).push(fn);
  }
  send(data: string) {
    this.sent.push(data);
  }
  close() {
    this.closed = true;
    this.emit("close", undefined);
  }
  emit(event: string, ev: any) {
    for (const fn of this.handlers[event] ?? []) fn(ev);
  }
  /** Simulate an inbound text frame. */
  inbound(obj: unknown) {
    this.emit("message", JSON.stringify(obj));
  }
}

const b64 = (s: string) => Buffer.from(s).toString("base64");

test("send wraps in data envelope", () => {
  const ws = new FakeWS();
  new Session(ws).send(new TextEncoder().encode("ls -la\n"));
  const env = JSON.parse(ws.sent[0]);
  assert.equal(env.type, "data");
  assert.equal(Buffer.from(env.b64, "base64").toString(), "ls -la\n");
});

test("sendText", () => {
  const ws = new FakeWS();
  new Session(ws).sendText("hi");
  assert.equal(Buffer.from(JSON.parse(ws.sent[0]).b64, "base64").toString(), "hi");
});

test("resize envelope", () => {
  const ws = new FakeWS();
  new Session(ws).resize(40, 120);
  assert.deepEqual(JSON.parse(ws.sent[0]), { type: "resize", rows: 40, cols: 120 });
});

test("hello populates attached, recv decodes data", async () => {
  const ws = new FakeWS();
  const s = new Session(ws);
  ws.inbound({ type: "hello", attached: [{ label: "me", joined_at: "t" }] });
  assert.equal(s.attached.length, 1);
  ws.inbound({ type: "data", b64: b64("output") });
  assert.equal(new TextDecoder().decode((await s.recv())!), "output");
});

test("recv waits for a later chunk", async () => {
  const ws = new FakeWS();
  const s = new Session(ws);
  const p = s.recv();
  ws.inbound({ type: "data", b64: b64("late") });
  assert.equal(new TextDecoder().decode((await p)!), "late");
});

test("recv returns null on close", async () => {
  const ws = new FakeWS();
  const s = new Session(ws);
  const p = s.recv();
  ws.close();
  assert.equal(await p, null);
  assert.equal(await s.recv(), null);
});

test("onData / onClose callbacks", async () => {
  const ws = new FakeWS();
  const s = new Session(ws);
  const chunks: string[] = [];
  let closed = false;
  s.onData((b) => chunks.push(new TextDecoder().decode(b)));
  s.onClose(() => (closed = true));
  ws.inbound({ type: "data", b64: b64("x") });
  ws.close();
  assert.deepEqual(chunks, ["x"]);
  assert.equal(closed, true);
});

test("error envelope fires onError and closes", async () => {
  const ws = new FakeWS();
  const s = new Session(ws);
  let err: DejimaError | undefined;
  s.onError((e) => (err = e));
  ws.inbound({ type: "error", b64: "session revoked" });
  assert.ok(err instanceof DejimaError);
  assert.match(err.description, /session revoked/);
  assert.equal(await s.recv(), null);
});

test("exit envelope ends the session without a socket close", async () => {
  // `exit` means the terminal ended, not that the link dropped. A caller that
  // can't tell them apart reconnects forever and respawns a shell nobody can
  // escape — so the session must finish here, while the socket is still open.
  const ws = new FakeWS();
  const s = new Session(ws);
  let closed = false;
  s.onClose(() => (closed = true));
  ws.inbound({ type: "exit" });
  assert.equal(closed, true);
  assert.equal(ws.closed, false);
  assert.equal(await s.recv(), null);
});

test("close() closes the socket", () => {
  const ws = new FakeWS();
  new Session(ws).close();
  assert.equal(ws.closed, true);
});
