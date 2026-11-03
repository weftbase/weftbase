/** ApiError is what every client throws for a failed call. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string
  ) {
    super(message)
    this.name = "ApiError"
  }
}

export function isNotFound(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404
}
