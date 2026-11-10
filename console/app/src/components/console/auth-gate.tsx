import { useQuery, useQueryClient } from "@tanstack/react-query"
import * as React from "react"

import { AppShell } from "@/components/console/app-shell"
import { ErrorState } from "@/components/console/empty-state"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { WeftbaseBrand } from "@/components/ui/weftbase-logo"
import { signInFromLink, tokenFromPaste } from "@/lib/api/auth"
import { isUnauthorized } from "@/lib/api/errors"
import { api } from "@/lib/api/http"
import { q } from "@/lib/api/queries"

// The Console's front door. In order:
// 1. if the address carries a one-time sign-in token (#signin=…), take it
//    out of the address and exchange it for the session cookie;
// 2. ask the server who we are (GET /v1/meta);
// 3. 401 → the signed-out screen; anything else that fails → an error
//    with a retry; success → the shell.
// A 401 on any later request sends the app back through step 2
// (router.tsx invalidates ["meta"]).
export function AuthGate() {
  const qc = useQueryClient()
  const [linking, setLinking] = React.useState(true)
  const [linkError, setLinkError] = React.useState<unknown>(null)

  React.useEffect(() => {
    let live = true
    signInFromLink()
      .catch((e: unknown) => live && setLinkError(e))
      .finally(() => live && setLinking(false))
    return () => {
      live = false
    }
  }, [])

  const meta = useQuery({ ...q.meta(), enabled: !linking })

  if (meta.error && isUnauthorized(meta.error))
    return (
      <SignedOut
        linkError={linkError}
        onSignedIn={() => {
          setLinkError(null)
          void qc.invalidateQueries({ queryKey: ["meta"] })
        }}
      />
    )
  if (meta.data) return <AppShell meta={meta.data} />
  if (meta.error)
    return (
      <Centered>
        <ErrorState
          error={meta.error}
          what="The server"
          onRetry={() => void meta.refetch()}
        />
      </Centered>
    )
  return (
    <Centered>
      <p role="status" className="text-xs text-muted-foreground">
        Connecting…
      </p>
    </Centered>
  )
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex h-svh flex-col items-center justify-center gap-4 bg-background p-4">
      <WeftbaseBrand />
      {children}
    </div>
  )
}

function SignedOut({
  linkError,
  onSignedIn,
}: {
  linkError: unknown
  onSignedIn: () => void
}) {
  const [text, setText] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState<unknown>(linkError)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const token = tokenFromPaste(text)
    if (!token) return
    setBusy(true)
    setError(null)
    try {
      await api.signIn(token)
      setText("")
      onSignedIn()
    } catch (err) {
      setError(err)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Centered>
      <div className="w-full max-w-sm rounded-card border bg-card p-4 shadow-card">
        <h1 className="text-sm font-semibold">You are signed out</h1>
        <p className="mt-1 text-xs text-muted-foreground">
          Open the sign-in link your server printed when it started (
          <code className="font-mono">weftbase dev</code> prints one), or paste
          it here. A link works once and for 15 minutes.
        </p>
        <form onSubmit={(e) => void submit(e)} className="mt-3 flex gap-2">
          <Input
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="Paste the sign-in link"
            aria-label="Sign-in link"
            autoComplete="off"
            spellCheck={false}
            // The link is a credential: never shown back in clear.
            type="password"
          />
          <Button type="submit" disabled={busy || text.trim() === ""}>
            {busy ? "Signing in…" : "Sign in"}
          </Button>
        </form>
        {error !== null && error !== undefined && (
          <p role="alert" className="mt-2 text-xs text-danger-foreground">
            {error instanceof Error ? error.message : String(error)}
          </p>
        )}
      </div>
    </Centered>
  )
}
