import type {
  FetchQueryOptions,
  QueryClient,
  QueryKey,
} from "@tanstack/react-query"
import { notFound } from "@tanstack/react-router"

import { isNotFound } from "./api/client"

/** ensure loads a query for a route loader; a 404 from the API becomes
 * the router's not-found page. */
export async function ensure<T, TKey extends QueryKey>(
  qc: QueryClient,
  opts: FetchQueryOptions<T, Error, T, TKey>
): Promise<T> {
  try {
    return await qc.ensureQueryData(opts)
  } catch (e) {
    if (isNotFound(e)) throw notFound()
    throw e
  }
}
