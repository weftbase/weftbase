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

/** 503 unavailable: a store the server reads could not be read (run
 * reads, sessions, stats, the conversation, delete, admit). Nothing is
 * missing: retry. */
export function isUnavailable(err: unknown): boolean {
  return err instanceof ApiError && err.status === 503
}

/** What a refused action means, by the server's 409 code. */
const CONFLICTS: Record<string, string> = {
  already_finished: "The run has already finished: there is nothing to stop.",
  already_resumed:
    "The run was already resumed after its wait: act on the run that continued it.",
  not_parked: "The run is not waiting on a person any more.",
  not_running: "The run is not running any more: it cannot take a message.",
  session_busy:
    "The session already has a turn going (queued, running or waiting on a person): start the next one when it has ended.",
  session_settling:
    "The session's previous turn is still being recorded: try again in a moment.",
}

/** actionError: a failed action in a sentence. A known 409 reads as what
 * it means; a 503 as a store that is down; anything else keeps the
 * server's own words. */
export function actionError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 409 && CONFLICTS[err.code]) return CONFLICTS[err.code]
    if (isUnavailable(err))
      return "The store is unavailable: nothing was changed. Try again."
  }
  return err instanceof Error ? err.message : String(err)
}
