import { DejimaError } from "./errors.js";
import { Session, WebSocketLike } from "./session.js";

const DEFAULT_HOST = "127.0.0.1:7273";

export interface ClientOptions {
  /** `HOST:PORT` of the daemon (default `$DEJIMA_HOST` or 127.0.0.1:7273). */
  host?: string;
  /** Per-island bearer token (default `$DEJIMA_TOKEN`). */
  token?: string;
  /** `http` (default) or `https`. */
  scheme?: "http" | "https";
  /** Per-request timeout in milliseconds (default 30000). */
  timeoutMs?: number;
  /** Override the `fetch` implementation (for testing or older runtimes). */
  fetch?: typeof fetch;
  /** Override how the PTY WebSocket is opened (for testing or the browser). */
  wsFactory?: (url: string, opts: { headers?: Record<string, string> }) => WebSocketLike;
}

type Query = Record<string, string | number | boolean | undefined>;

/** Drop `undefined` values so optional fields are simply omitted from the body. */
function clean<T extends Record<string, unknown>>(obj: T): Partial<T> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(obj)) if (v !== undefined) out[k] = v;
  return out as Partial<T>;
}

function env(name: string): string | undefined {
  const g: any = globalThis as any;
  return typeof g.process !== "undefined" ? g.process.env?.[name] : undefined;
}

/** A client for one Dejima daemon. Mirrors the Python `dejima.Client`. */
export class Client {
  readonly host: string;
  readonly token?: string;
  readonly scheme: "http" | "https";
  readonly base: string;
  private readonly timeoutMs: number;
  private readonly fetchImpl: typeof fetch;
  private readonly wsFactory?: ClientOptions["wsFactory"];

  constructor(opts: ClientOptions = {}) {
    this.host = opts.host ?? env("DEJIMA_HOST") ?? DEFAULT_HOST;
    this.token = opts.token ?? env("DEJIMA_TOKEN");
    this.scheme = opts.scheme ?? "http";
    this.base = `${this.scheme}://${this.host}`;
    this.timeoutMs = opts.timeoutMs ?? 30000;
    const f = opts.fetch ?? (globalThis as any).fetch;
    if (!f) throw new Error("no fetch available; pass options.fetch");
    this.fetchImpl = f.bind(globalThis);
    this.wsFactory = opts.wsFactory;
  }

  // ----- low-level ------------------------------------------------------
  private url(path: string, query?: Query): string {
    let u = this.base + path;
    if (query) {
      const qs = Object.entries(query)
        .filter(([, v]) => v !== undefined)
        .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
        .join("&");
      if (qs) u += `?${qs}`;
    }
    return u;
  }

  private async request(
    method: string,
    path: string,
    opts: { json?: unknown; body?: BodyInit; query?: Query; accept?: string } = {},
  ): Promise<Response> {
    const headers: Record<string, string> = {};
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;
    if (opts.accept) headers["Accept"] = opts.accept;
    let body: BodyInit | undefined = opts.body;
    if (opts.json !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(opts.json);
    }
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), this.timeoutMs);
    let resp: Response;
    try {
      resp = await this.fetchImpl(this.url(path, opts.query), { method, headers, body, signal: ctrl.signal });
    } finally {
      clearTimeout(timer);
    }
    if (!resp.ok) throw new DejimaError(resp.status, await errorMessage(resp));
    return resp;
  }

  private async json<T = any>(method: string, path: string, opts: Parameters<Client["request"]>[2] = {}): Promise<T> {
    const r = await this.request(method, path, opts);
    const text = await r.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  private seg(s: string): string {
    return encodeURIComponent(s);
  }

  private island(name: string): string {
    return `/v1/islands/${this.seg(name)}`;
  }

  // ===== islands ========================================================
  /** All islands, most-recently-used first. */
  listIslands(): Promise<any[]> {
    return this.json("GET", "/v1/islands");
  }

  /** Provision an island. `repo` is a git URL or local path. */
  createIsland(
    repo: string,
    opts: {
      agent?: string;
      name?: string;
      image?: string;
      cmd?: string;
      resources?: Record<string, unknown>;
      agents?: Array<Record<string, unknown>>;
      role?: string;
      githubIdentity?: string;
      seedPath?: string;
      owner?: string;
      tags?: Record<string, string>;
    } = {},
  ): Promise<any> {
    const body = clean({
      repo,
      agent: opts.agent,
      name: opts.name,
      image: opts.image,
      cmd: opts.cmd,
      resources: opts.resources,
      agents: opts.agents,
      role: opts.role,
      github_identity: opts.githubIdentity,
      seed_path: opts.seedPath,
      owner: opts.owner,
      tags: opts.tags,
    });
    return this.json("POST", "/v1/islands", { json: body });
  }

  /** Island detail (agents, presence, cpu/mem, git, health, resources, disk). */
  getIsland(name: string): Promise<any> {
    return this.json("GET", this.island(name));
  }

  /** Update the island's cosmetic display title (`""` clears it). */
  updateIsland(name: string, title: string): Promise<any> {
    return this.json("PATCH", this.island(name), { json: { title } });
  }

  /** Purge an island. `force` bypasses the unpushed-work guard. */
  async deleteIsland(name: string, force = false): Promise<void> {
    await this.request("DELETE", this.island(name), { query: force ? { force: true } : undefined });
  }

  /** Stop the container, preserving volumes. */
  hibernate(name: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/hibernate`);
  }

  /** Start a hibernated island's container against existing volumes. */
  wake(name: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/wake`);
  }

  /** Clear agent on-disk state (preserves the workspace). */
  reset(name: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/reset`);
  }

  /** Recreate the container against the current island image (both volumes preserved). */
  upgrade(name: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/upgrade`);
  }

  /** Copy an island (workspace + home volumes; Port grants dropped). */
  cloneIsland(name: string, newName: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/clone`, { json: { new_name: newName } });
  }

  /** Update memory limit (live) and/or OOM priority (needs a recreate). */
  setResources(name: string, opts: { memory?: string; oomPriority?: number }): Promise<any> {
    const body: Record<string, unknown> = {};
    if (opts.memory !== undefined) body.memory = opts.memory;
    if (opts.oomPriority !== undefined) body.oom_priority = opts.oomPriority;
    return this.json("PUT", `${this.island(name)}/resources`, { json: body });
  }

  /** Whether the island's repo clone has landed in /workspace yet. */
  async workspaceReady(name: string): Promise<boolean> {
    const r = await this.json("GET", `${this.island(name)}/workspace-ready`);
    return Boolean(r?.ready);
  }

  /** The island's recent event log (newest first). */
  islandEvents(name: string): Promise<any[]> {
    return this.json("GET", `${this.island(name)}/events`);
  }

  // ===== agents =========================================================
  listAgents(name: string): Promise<any[]> {
    return this.json("GET", `${this.island(name)}/agents`);
  }

  /** Add an agent (its own worktree + tmux session). */
  addAgent(
    name: string,
    opts: { type?: string; label?: string; cmd?: string; provider?: string; model?: string } = {},
  ): Promise<any> {
    return this.json("POST", `${this.island(name)}/agents`, { json: clean({ ...opts }) });
  }

  getAgent(name: string, agentId: string): Promise<any> {
    return this.json("GET", `${this.island(name)}/agents/${this.seg(agentId)}`);
  }

  /** Rename an agent's cosmetic label (`""` clears it). */
  updateAgent(name: string, agentId: string, label: string): Promise<any> {
    return this.json("PATCH", `${this.island(name)}/agents/${this.seg(agentId)}`, { json: { label } });
  }

  /** Remove a non-primary agent (kills its session, prunes its worktree). */
  async removeAgent(name: string, agentId: string): Promise<void> {
    await this.request("DELETE", `${this.island(name)}/agents/${this.seg(agentId)}`);
  }

  /** Set an agent's LLM provider/model (key-requiring frameworks only). */
  configureAgent(
    name: string,
    agentId: string,
    opts: { provider?: string; model?: string },
  ): Promise<any> {
    const body: Record<string, unknown> = {};
    if (opts.provider !== undefined) body.provider = opts.provider;
    if (opts.model !== undefined) body.model = opts.model;
    return this.json("PATCH", `${this.island(name)}/agents/${this.seg(agentId)}/config`, { json: body });
  }

  // ===== exec / files / logs -------------------------------------------
  /** Run a one-shot command. Returns `{stdout, stderr, exit_code}`. */
  exec(name: string, cmd: string[]): Promise<any> {
    return this.json("POST", `${this.island(name)}/exec`, { json: { cmd } });
  }

  /** Read a file from the island (defaults to the primary worktree). */
  async readFile(name: string, path: string): Promise<Uint8Array> {
    const r = await this.request("GET", `${this.island(name)}/files/${encodePath(path)}`);
    return new Uint8Array(await r.arrayBuffer());
  }

  /** Write a file into the island. */
  async writeFile(name: string, path: string, data: Uint8Array | string): Promise<void> {
    await this.request("PUT", `${this.island(name)}/files/${encodePath(path)}`, { body: data as BodyInit });
  }

  /** Fetch container/agent logs as text. */
  async logs(name: string, opts: { agent?: string } = {}): Promise<string> {
    const r = await this.request("GET", `${this.island(name)}/logs`, { query: clean({ agent: opts.agent }) });
    return r.text();
  }

  // ===== port broker ----------------------------------------------------
  listPortScopes(name: string): Promise<any> {
    return this.json("GET", `${this.island(name)}/port/scopes`);
  }

  grantPortScope(name: string, hostPath: string, mode: "ro" | "rw" = "ro"): Promise<any> {
    return this.json("POST", `${this.island(name)}/port/scopes`, { json: { host_path: hostPath, mode } });
  }

  async revokePortScope(name: string, scope: string): Promise<void> {
    await this.request("DELETE", `${this.island(name)}/port/scopes/${this.seg(scope)}`);
  }

  portIntake(name: string, scope: string, srcRel: string, dest?: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/port/intake`, { json: clean({ scope, src_rel: srcRel, dest }) });
  }

  portExport(name: string, src: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/port/export`, { json: { src } });
  }

  portWrite(name: string, scope: string, src: string, destRel: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/port/write`, { json: { scope, src, dest_rel: destRel } });
  }

  // ===== capability broker ---------------------------------------------
  listCapabilityGrants(name: string): Promise<any> {
    return this.json("GET", `${this.island(name)}/capability/grants`);
  }

  grantCapability(name: string, target: string): Promise<any> {
    return this.json("POST", `${this.island(name)}/capability/grants`, { json: { target } });
  }

  async revokeCapability(name: string, target: string): Promise<void> {
    await this.request("DELETE", `${this.island(name)}/capability/grants/${this.seg(target)}`);
  }

  /** Invoke a granted capability. Operators name `island`; an in-island token is pinned. */
  executeCapability(target: string, opts: { island?: string; args?: Record<string, string> } = {}): Promise<any> {
    return this.json("POST", "/v1/capabilities/execute", { json: clean({ target, island: opts.island, args: opts.args }) });
  }

  // ===== credentials ----------------------------------------------------
  async pushClaudeCredentials(credentialsJson: string): Promise<void> {
    await this.request("PUT", "/v1/credentials/claude", { json: { credentials_json: credentialsJson } });
  }

  claudeCredentialsStatus(): Promise<any> {
    return this.json("GET", "/v1/credentials/claude");
  }

  listGitHubIdentities(): Promise<any> {
    return this.json("GET", "/v1/credentials/github");
  }

  putGitHubIdentity(
    name: string,
    opts: { login: string; token: string; id?: number; host?: string; default?: boolean },
  ): Promise<any> {
    const body = clean({ login: opts.login, token: opts.token, id: opts.id, host: opts.host, default: opts.default || undefined });
    return this.json("PUT", `/v1/credentials/github/${this.seg(name)}`, { json: body });
  }

  deleteGitHubIdentity(name: string): Promise<any> {
    return this.json("DELETE", `/v1/credentials/github/${this.seg(name)}`);
  }

  githubRepos(name: string): Promise<any> {
    return this.json("GET", `/v1/credentials/github/${this.seg(name)}/repos`);
  }

  listProviders(): Promise<any> {
    return this.json("GET", "/v1/credentials/providers");
  }

  putProvider(
    provider: string,
    opts: { apiKey: string; baseUrl?: string; envVar?: string; default?: boolean },
  ): Promise<any> {
    const body = clean({ api_key: opts.apiKey, base_url: opts.baseUrl, env_var: opts.envVar, default: opts.default || undefined });
    return this.json("PUT", `/v1/credentials/providers/${this.seg(provider)}`, { json: body });
  }

  deleteProvider(provider: string): Promise<any> {
    return this.json("DELETE", `/v1/credentials/providers/${this.seg(provider)}`);
  }

  // ===== events / webhooks ---------------------------------------------
  listSubscriptions(): Promise<any[]> {
    return this.json("GET", "/v1/events/subscriptions");
  }

  subscribeWebhook(url: string, opts: { secret?: string; events?: string[] } = {}): Promise<any> {
    return this.json("POST", "/v1/events/subscribe", { json: clean({ url, secret: opts.secret, events: opts.events }) });
  }

  async unsubscribeWebhook(subscriptionId: string): Promise<void> {
    await this.request("DELETE", `/v1/events/subscriptions/${this.seg(subscriptionId)}`);
  }

  // ===== daemon ---------------------------------------------------------
  overview(): Promise<any> {
    return this.json("GET", "/v1/overview");
  }

  agentTypes(): Promise<any> {
    return this.json("GET", "/v1/agent-types");
  }

  /** Reachability probe; resolves true when the daemon answers. */
  async healthz(): Promise<boolean> {
    try {
      await this.request("GET", "/v1/healthz");
      return true;
    } catch (e) {
      if (e instanceof DejimaError) return false;
      throw e;
    }
  }

  /** The brokered-operation ledger + a hash-chain verification verdict. */
  audit(opts: { limit?: number } = {}): Promise<any> {
    return this.json("GET", "/v1/audit", { query: clean({ limit: opts.limit }) });
  }

  /** Recent attach/detach events across every island (newest first). */
  clientHistory(): Promise<any[]> {
    return this.json("GET", "/v1/clients");
  }

  /** Drop every active websocket client across every island. */
  revokeSessions(): Promise<any> {
    return this.json("POST", "/v1/sessions/revoke");
  }

  panicStatus(): Promise<any> {
    return this.json("GET", "/v1/panic");
  }

  panic(opts: { reason?: string } = {}): Promise<any> {
    return this.json("POST", "/v1/panic", { json: clean({ reason: opts.reason }) });
  }

  unpanic(): Promise<any> {
    return this.json("DELETE", "/v1/panic");
  }

  adminUpdate(opts: { execute?: boolean; force?: boolean } = {}): Promise<any> {
    return this.json("POST", "/v1/admin/update", { json: { execute: opts.execute ?? false, force: opts.force ?? false } });
  }

  /** Rebuild the island image; resolves the combined build output as text. */
  async imageBuild(): Promise<string> {
    const r = await this.request("POST", "/v1/image/build");
    return r.text();
  }

  authorizeSshKey(publicKey: string): Promise<any> {
    return this.json("POST", "/v1/ssh/account-keys", { json: { public_key: publicKey } });
  }

  listSshKeys(): Promise<any> {
    return this.json("GET", "/v1/ssh/account-keys");
  }

  // ===== host terminals (operator-only; requires --host-terminals) -----
  listTerminals(): Promise<any> {
    return this.json("GET", "/v1/terminals");
  }

  createTerminal(opts: { label?: string } = {}): Promise<any> {
    return this.json("POST", "/v1/terminals", { json: clean({ label: opts.label }) });
  }

  async deleteTerminal(terminalId: string): Promise<void> {
    await this.request("DELETE", `/v1/terminals/${this.seg(terminalId)}`);
  }

  relabelTerminal(terminalId: string, label: string): Promise<any> {
    return this.json("PATCH", `/v1/terminals/${this.seg(terminalId)}`, { json: { label } });
  }

  // ===== session (WebSocket PTY) ---------------------------------------
  /** The WebSocket URL for an interactive PTY session (multi-attach). */
  sessionUrl(name: string, opts: { agent?: string } = {}): string {
    const base = this.wsBase();
    return opts.agent
      ? `${base}${this.island(name)}/agents/${this.seg(opts.agent)}/session`
      : `${base}${this.island(name)}/session`;
  }

  /** The WebSocket URL for a host terminal's PTY session. */
  terminalSessionUrl(terminalId: string, opts: { label?: string } = {}): string {
    let url = `${this.wsBase()}/v1/terminals/${this.seg(terminalId)}/session`;
    if (opts.label) url += `?label=${encodeURIComponent(opts.label)}`;
    return url;
  }

  private wsBase(): string {
    return `${this.scheme === "https" ? "wss" : "ws"}://${this.host}`;
  }

  /** Open an island's PTY session stream. Resolves once the socket is open. */
  attach(name: string, opts: { agent?: string } = {}): Promise<Session> {
    return this.openSession(this.sessionUrl(name, opts));
  }

  /** Open a host terminal's PTY session stream. */
  attachTerminal(terminalId: string, opts: { label?: string } = {}): Promise<Session> {
    return this.openSession(this.terminalSessionUrl(terminalId, opts));
  }

  private async openSession(url: string): Promise<Session> {
    const headers: Record<string, string> = {};
    if (this.token) headers["Authorization"] = `Bearer ${this.token}`;

    let ws: WebSocketLike;
    if (this.wsFactory) {
      ws = this.wsFactory(url, { headers });
    } else {
      ws = await defaultWebSocket(url, headers);
    }
    await waitOpen(ws);
    return new Session(ws);
  }
}

function encodePath(path: string): string {
  // Keep slashes (the daemon matches {path...}) but escape each segment.
  return path.split("/").map(encodeURIComponent).join("/");
}

async function errorMessage(resp: Response): Promise<string> {
  const body = (await resp.text()).trim();
  try {
    const data = JSON.parse(body);
    if (data && typeof data.error === "string") return data.error;
  } catch {
    /* not JSON */
  }
  return body || resp.statusText;
}

/**
 * Open a WebSocket using the Node `ws` package when available (it can set the
 * Authorization header), falling back to the global `WebSocket` (browser / Node
 * 22+ — note that the global cannot set request headers, so token auth needs the
 * `ws` package or operator/no-token access).
 */
async function defaultWebSocket(url: string, headers: Record<string, string>): Promise<WebSocketLike> {
  try {
    const mod: any = await import("ws");
    const WS = mod.default ?? mod.WebSocket ?? mod;
    return new WS(url, { headers }) as WebSocketLike;
  } catch {
    const G: any = globalThis as any;
    if (typeof G.WebSocket === "function") return new G.WebSocket(url) as WebSocketLike;
    throw new DejimaError(0, "no WebSocket available; install 'ws' or pass options.wsFactory");
  }
}

function waitOpen(ws: WebSocketLike): Promise<void> {
  return new Promise((resolve, reject) => {
    const ok = () => resolve();
    const bad = (e: any) => reject(new DejimaError(0, `websocket error: ${e?.message ?? e}`));
    if (typeof (ws as any).on === "function") {
      (ws as any).on("open", ok);
      (ws as any).on("error", bad);
    } else if (typeof ws.addEventListener === "function") {
      ws.addEventListener("open", ok);
      ws.addEventListener("error", bad);
    } else {
      resolve(); // injected fake with no events; assume ready
    }
  });
}
