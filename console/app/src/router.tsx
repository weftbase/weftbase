import { QueryCache, QueryClient } from "@tanstack/react-query"
import { createRouter as createTanStackRouter } from "@tanstack/react-router"
import type { LocationRewrite } from "@tanstack/react-router"

import { ApiError, isUnauthorized } from "./lib/api/errors"
import { routeTree } from "./routeTree.gen"

export interface RouterContext {
  queryClient: QueryClient
}

// The mount prefix comes from the shell's <base href>, which the Go
// handler rewrites per request, so one bundle serves "/" or "/weftbase/".
// It is applied as a URL rewrite, not as the router's basepath: Start
// re-applies its build-time basepath ("/") at hydration, and a rewrite
// survives that. The shell prerender has no document and no prefix.
export function mountRewrite(
  baseURI: string | undefined
): LocationRewrite | undefined {
  if (!baseURI) return undefined
  const prefix = new URL(baseURI).pathname.replace(/\/+$/, "")
  if (!prefix) return undefined
  return {
    input: ({ url }) => {
      if (url.pathname === prefix || url.pathname.startsWith(`${prefix}/`)) {
        url.pathname = url.pathname.slice(prefix.length) || "/"
      }
      return url
    },
    output: ({ url }) => {
      url.pathname = `${prefix}${url.pathname}`
      return url
    },
  }
}

export function getRouter() {
  const queryClient: QueryClient = new QueryClient({
    // A 401 on any request means the session ended: ask the server who
    // we are again, and the auth gate shows the signed-out screen.
    queryCache: new QueryCache({
      onError: (err, query) => {
        if (isUnauthorized(err) && query.queryKey[0] !== "meta")
          void queryClient.invalidateQueries({ queryKey: ["meta"] })
      },
    }),
    defaultOptions: {
      queries: {
        staleTime: 10_000,
        // A client error will not get better by asking again.
        retry: (n, err) =>
          !(err instanceof ApiError && err.status < 500) && n < 1,
      },
    },
  })
  return createTanStackRouter({
    routeTree,
    rewrite: mountRewrite(
      typeof document === "undefined" ? undefined : document.baseURI
    ),
    context: { queryClient },
    scrollRestoration: true,
    defaultPreload: "intent",
    defaultPreloadStaleTime: 0,
  })
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof getRouter>
  }
}
