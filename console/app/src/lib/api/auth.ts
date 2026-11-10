// Console sign-in, as the server implements it (api/http/sessions.go):
// `weftbase dev` prints a one-time link `<console>/#signin=<token>`. The
// Console takes the token out of the address at once, POSTs it to
// /v1/auth/sessions and the server sets the HttpOnly wb_session cookie.
//
// This file is the only one that reads the page address for a credential.
// The token is never written to storage, state, a log or another URL.

import { api } from "./http"
import type { SignInResponse } from "./types"

const PARAM = "signin"

/** takeSignInToken removes #signin=… from the address bar (and from the
 * history entry) and returns the token, or null when there is none. */
export function takeSignInToken(): string | null {
  if (typeof window === "undefined") return null
  const hash = window.location.hash.replace(/^#/, "")
  if (!hash) return null
  const params = new URLSearchParams(hash)
  const token = params.get(PARAM)
  if (token === null) return null
  params.delete(PARAM)
  const rest = params.toString()
  window.history.replaceState(
    window.history.state,
    "",
    window.location.pathname + window.location.search + (rest ? `#${rest}` : "")
  )
  return token || null
}

/** tokenFromPaste accepts what an operator pastes into the signed-out
 * form: the whole sign-in link, or the bare token. */
export function tokenFromPaste(text: string): string {
  const t = text.trim()
  const i = t.indexOf(`#${PARAM}=`)
  return i < 0 ? t : t.slice(i + PARAM.length + 2)
}

/** onSignInLink calls handle with the token of a sign-in link opened in
 * this tab after the page loaded. Opening `<console>/#signin=…` in a tab
 * already showing the Console changes only the hash: the page does not
 * reload, so the load-time exchange never sees it. Returns the cleanup. */
export function onSignInLink(handle: (token: string) => void): () => void {
  if (typeof window === "undefined") return () => {}
  const listener = () => {
    const token = takeSignInToken()
    if (token) handle(token)
  }
  window.addEventListener("hashchange", listener)
  return () => window.removeEventListener("hashchange", listener)
}

let exchange: Promise<SignInResponse | null> | undefined

/** signInFromLink runs the link exchange once per page load. It resolves
 * to null when the address carried no token. */
export function signInFromLink(): Promise<SignInResponse | null> {
  exchange ??= (async () => {
    const token = takeSignInToken()
    return token ? api.signIn(token) : null
  })()
  return exchange
}
