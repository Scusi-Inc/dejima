/**
 * Raised when the daemon returns a non-2xx response. The daemon's error body is
 * JSON (`{"error": "..."}`); {@link message} is the extracted text when present,
 * else the raw body.
 */
export class DejimaError extends Error {
  readonly status: number;
  /** Alias of {@link Error.message}, mirroring the Python client's `.message`. */
  readonly description: string;

  constructor(status: number, message: string) {
    super(`HTTP ${status}: ${message}`);
    this.name = "DejimaError";
    this.status = status;
    this.description = message;
  }
}
