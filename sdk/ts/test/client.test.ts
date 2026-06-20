import { test } from "node:test";
import assert from "node:assert/strict";
import { Client, DejimaError } from "../src/index.js";

interface Captured {
  method?: string;
  url?: string;
  headers?: Record<string, string>;
  body?: string;
}

/** Build a Client whose fetch records the request and returns a scripted response. */
function makeClient(resp: { status?: number; body?: unknown; text?: string; contentType?: string }) {
  const cap: Captured = {};
  const fetchImpl = (async (url: string, init: any) => {
    cap.method = init.method;
    cap.url = url;
    cap.headers = init.headers;
    cap.body = init.body;
    const status = resp.status ?? 200;
    const payload = resp.text !== undefined ? resp.text : resp.body !== undefined ? JSON.stringify(resp.body) : "";
    return {
      ok: status >= 200 && status < 300,
      status,
      statusText: "",
      text: async () => payload,
      arrayBuffer: async () => new TextEncoder().encode(payload).buffer,
    } as any;
  }) as unknown as typeof fetch;
  const dj = new Client({ host: "h:1", token: "tok-123", fetch: fetchImpl });
  return { dj, cap };
}

test("auth header present", async () => {
  const { dj, cap } = makeClient({ body: [] });
  await dj.listIslands();
  assert.equal(cap.headers!["Authorization"], "Bearer tok-123");
});

test("listIslands GET", async () => {
  const { dj, cap } = makeClient({ body: [{ name: "foo" }] });
  const out = await dj.listIslands();
  assert.equal(cap.method, "GET");
  assert.equal(cap.url, "http://h:1/v1/islands");
  assert.deepEqual(out, [{ name: "foo" }]);
});

test("createIsland drops undefined fields", async () => {
  const { dj, cap } = makeClient({ status: 201, body: { name: "foo" } });
  await dj.createIsland("git@github.com:you/foo.git", { agent: "claude-code", role: "home" });
  assert.equal(cap.method, "POST");
  const sent = JSON.parse(cap.body!);
  assert.deepEqual(sent, { repo: "git@github.com:you/foo.git", agent: "claude-code", role: "home" });
});

test("createIsland maps githubIdentity -> github_identity", async () => {
  const { dj, cap } = makeClient({ status: 201, body: {} });
  await dj.createIsland("repo", { githubIdentity: "work", seedPath: "/tmp/x" });
  const sent = JSON.parse(cap.body!);
  assert.equal(sent.github_identity, "work");
  assert.equal(sent.seed_path, "/tmp/x");
  assert.ok(!("githubIdentity" in sent));
});

test("getIsland encodes name segment", async () => {
  const { dj, cap } = makeClient({ body: {} });
  await dj.getIsland("a/b");
  assert.equal(cap.url, "http://h:1/v1/islands/a%2Fb");
});

test("deleteIsland force query", async () => {
  const { dj, cap } = makeClient({ status: 204 });
  await dj.deleteIsland("foo", true);
  assert.equal(cap.method, "DELETE");
  assert.equal(cap.url, "http://h:1/v1/islands/foo?force=true");
});

test("deleteIsland no force", async () => {
  const { dj, cap } = makeClient({ status: 204 });
  await dj.deleteIsland("foo");
  assert.equal(cap.url, "http://h:1/v1/islands/foo");
});

test("setResources sends both fields", async () => {
  const { dj, cap } = makeClient({ body: { restart_required: true } });
  const out = await dj.setResources("foo", { memory: "4g", oomPriority: 100 });
  assert.equal(cap.method, "PUT");
  assert.equal(cap.url, "http://h:1/v1/islands/foo/resources");
  assert.deepEqual(JSON.parse(cap.body!), { memory: "4g", oom_priority: 100 });
  assert.equal(out.restart_required, true);
});

test("setResources unlimited memory keeps empty string", async () => {
  const { dj, cap } = makeClient({ body: {} });
  await dj.setResources("foo", { memory: "" });
  assert.deepEqual(JSON.parse(cap.body!), { memory: "" });
});

test("workspaceReady", async () => {
  const { dj, cap } = makeClient({ body: { ready: true } });
  assert.equal(await dj.workspaceReady("foo"), true);
  assert.equal(cap.url, "http://h:1/v1/islands/foo/workspace-ready");
});

test("exec body", async () => {
  const { dj, cap } = makeClient({ body: { exit_code: 0 } });
  await dj.exec("foo", ["git", "status"]);
  assert.deepEqual(JSON.parse(cap.body!), { cmd: ["git", "status"] });
});

test("readFile keeps slashes in path", async () => {
  const { dj, cap } = makeClient({ text: "file-bytes", contentType: "application/octet-stream" });
  const bytes = await dj.readFile("foo", "src/main.go");
  assert.equal(new TextDecoder().decode(bytes), "file-bytes");
  assert.equal(cap.url, "http://h:1/v1/islands/foo/files/src/main.go");
});

test("logs with agent query", async () => {
  const { dj, cap } = makeClient({ text: "log lines" });
  assert.equal(await dj.logs("foo", { agent: "p2" }), "log lines");
  assert.equal(cap.url, "http://h:1/v1/islands/foo/logs?agent=p2");
});

test("configureAgent path + body", async () => {
  const { dj, cap } = makeClient({ body: { restart_required: true } });
  await dj.configureAgent("foo", "p1", { provider: "anthropic", model: "opus" });
  assert.equal(cap.url, "http://h:1/v1/islands/foo/agents/p1/config");
  assert.deepEqual(JSON.parse(cap.body!), { provider: "anthropic", model: "opus" });
});

test("grant/revoke port scope", async () => {
  let { dj, cap } = makeClient({ status: 201, body: { name: "scope-1" } });
  await dj.grantPortScope("foo", "/Users/me/data", "ro");
  assert.deepEqual(JSON.parse(cap.body!), { host_path: "/Users/me/data", mode: "ro" });

  ({ dj, cap } = makeClient({ status: 204 }));
  await dj.revokePortScope("foo", "scope-1");
  assert.equal(cap.method, "DELETE");
  assert.equal(cap.url, "http://h:1/v1/islands/foo/port/scopes/scope-1");
});

test("executeCapability clean body", async () => {
  const { dj, cap } = makeClient({ body: { ok: true } });
  await dj.executeCapability("notify", { args: { msg: "hi" } });
  assert.equal(cap.url, "http://h:1/v1/capabilities/execute");
  assert.deepEqual(JSON.parse(cap.body!), { target: "notify", args: { msg: "hi" } });
});

test("putProvider omits default when false", async () => {
  let { dj, cap } = makeClient({ body: {} });
  await dj.putProvider("anthropic", { apiKey: "sk-x", default: true });
  assert.deepEqual(JSON.parse(cap.body!), { api_key: "sk-x", default: true });

  ({ dj, cap } = makeClient({ body: {} }));
  await dj.putProvider("anthropic", { apiKey: "sk-x" });
  assert.deepEqual(JSON.parse(cap.body!), { api_key: "sk-x" });
});

test("audit limit query", async () => {
  const { dj, cap } = makeClient({ body: { total: 0, returned: 0, verified: true } });
  await dj.audit({ limit: 5 });
  assert.equal(cap.url, "http://h:1/v1/audit?limit=5");
});

test("audit filters", async () => {
  const { dj, cap } = makeClient({ body: { total: 0, returned: 0, verified: true } });
  await dj.audit({ island: "web", typePrefix: "port.", actor: "alice", decision: "denied" });
  assert.match(cap.url!, /island=web/);
  assert.match(cap.url!, /type=port\./);
  assert.match(cap.url!, /actor=alice/);
  assert.match(cap.url!, /decision=denied/);
});

test("audit csv returns text", async () => {
  const { dj, cap } = makeClient({ text: "seq,type\n1,port.grant\n" });
  const out = await dj.audit({ format: "csv" });
  assert.equal(out, "seq,type\n1,port.grant\n");
  assert.match(cap.url!, /format=csv/);
});

test("mcp grant / list / revoke", async () => {
  let { dj, cap } = makeClient({ body: { grants: [] } });
  await dj.listMcpGrants("foo");
  assert.equal(cap.url, "http://h:1/v1/islands/foo/mcp/grants");

  ({ dj, cap } = makeClient({ status: 201, body: { server: "files" } }));
  await dj.grantMcp("foo", "files");
  assert.deepEqual(JSON.parse(cap.body!), { server: "files" });

  ({ dj, cap } = makeClient({ status: 204 }));
  await dj.revokeMcp("foo", "files");
  assert.equal(cap.method, "DELETE");
  assert.equal(cap.url, "http://h:1/v1/islands/foo/mcp/grants/files");
});

test("mcpCall body", async () => {
  const { dj, cap } = makeClient({ body: { ok: true } });
  await dj.mcpCall("files", "tools/list", { params: { x: 1 } });
  assert.equal(cap.url, "http://h:1/v1/mcp/call");
  assert.deepEqual(JSON.parse(cap.body!), { server: "files", method: "tools/list", params: { x: 1 } });
});

test("createToken returns secret once", async () => {
  const { dj, cap } = makeClient({ status: 201, body: { token: { id: "t1" }, secret: "sk-once" } });
  const out = await dj.createToken("operator", { label: "ci", islands: ["web"] });
  assert.equal(cap.method, "POST");
  assert.equal(cap.url, "http://h:1/v1/tokens");
  assert.deepEqual(JSON.parse(cap.body!), { role: "operator", label: "ci", islands: ["web"] });
  assert.equal(out.secret, "sk-once");
});

test("list / revoke token", async () => {
  let { dj, cap } = makeClient({ body: { tokens: [] } });
  await dj.listTokens();
  assert.equal(cap.url, "http://h:1/v1/tokens");

  ({ dj, cap } = makeClient({ status: 204 }));
  await dj.revokeToken("t1");
  assert.equal(cap.method, "DELETE");
  assert.equal(cap.url, "http://h:1/v1/tokens/t1");
});

test("healthz true / false", async () => {
  let { dj } = makeClient({ status: 200, body: { status: "ok" } });
  assert.equal(await dj.healthz(), true);
  ({ dj } = makeClient({ status: 503, body: { error: "down" } }));
  assert.equal(await dj.healthz(), false);
});

test("error decodes json message", async () => {
  const { dj } = makeClient({ status: 409, body: { error: "unpushed work" } });
  await assert.rejects(dj.deleteIsland("foo"), (e: unknown) => {
    assert.ok(e instanceof DejimaError);
    assert.equal(e.status, 409);
    assert.equal(e.description, "unpushed work");
    return true;
  });
});

test("error falls back to text", async () => {
  const { dj } = makeClient({ status: 500, text: "boom" });
  await assert.rejects(dj.listIslands(), (e: unknown) => {
    assert.ok(e instanceof DejimaError);
    assert.equal(e.description, "boom");
    return true;
  });
});

test("session urls", () => {
  const https = new Client({ host: "h:1", scheme: "https", fetch: (() => {}) as any });
  assert.equal(https.sessionUrl("foo"), "wss://h:1/v1/islands/foo/session");
  assert.equal(https.sessionUrl("foo", { agent: "p2" }), "wss://h:1/v1/islands/foo/agents/p2/session");

  const http = new Client({ host: "h:1", fetch: (() => {}) as any });
  assert.ok(http.sessionUrl("foo").startsWith("ws://"));
  assert.equal(http.terminalSessionUrl("t1", { label: "me" }), "ws://h:1/v1/terminals/t1/session?label=me");
});
