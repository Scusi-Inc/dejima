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
 * Alpha (0.x): fields may change until 1.0. The REST layer mirrors
 * `openapi.yaml`; the only hand-written ergonomic piece is the PTY `Session`.
 */
export { Client } from "./client.js";
export type { ClientOptions } from "./client.js";
export { Session } from "./session.js";
export type { SessionEnvelope, WebSocketLike } from "./session.js";
export { DejimaError } from "./errors.js";

export const VERSION = "0.1.0";
