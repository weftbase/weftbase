/** ApiError is what every API call throws for a failed request. The
 * server's error shape is {"error": {"code", "message", "request_id"?}}. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly requestId?: string
  ) {
    super(message)
    this.name = "ApiError"
  }
}

export function isNotFound(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404
}

/** 401: no session, or it ended. The shell shows the signed-out state. */
export function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401
}

/** 501: this server cannot do that (no registry, no ledger, a database
 * without the capability). Screens say so instead of failing. */
export function isUnsupported(err: unknown): boolean {
  return err instanceof ApiError && err.status === 501
}
