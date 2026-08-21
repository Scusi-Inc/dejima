/**
 * Dejima — TypeScript/JavaScript client for the Dejima API.
 *
 * ```ts
 * import { Client } from "@dejima/sdk";
 * const dj = new Client();                 // reads DEJIMA_HOST / DEJIMA_TOKEN
 * const isl = await dj.createIsland("git@github.com:you/foo.git", { agent: "claude-code" });
 * console.log(await dj.listIslands());
 * ```
 *
 * Alpha (0.x): fields may change until 1.0.
 *
 * THIS CLIENT IS HAND-WRITTEN, not generated. It is kept in step with
 * `openapi.yaml` by hand, and it lags: methods return `any` rather than domain
 * types, and a field documented in the spec does not reach you until someone
 * adds it here. An earlier version of this comment said the REST layer
 * "mirrors openapi.yaml", which reads as generated-and-therefore-current and
 * is what a client author would reasonably rely on.
 *
 * If you want types, generate them from `openapi.yaml` directly — the spec is
 * gated against the daemon on every commit (sdk/openapi_field_parity.py), so it
 * is the trustworthy source. This client is not.
 */
export { Client } from "./client.js";
export type { ClientOptions } from "./client.js";
export { Session } from "./session.js";
export type { SessionEnvelope, WebSocketLike } from "./session.js";
export { DejimaError } from "./errors.js";

export const VERSION = "0.1.0";
