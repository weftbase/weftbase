import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate, useRouter } from "@tanstack/react-router"
import {
  CheckIcon,
  DownloadIcon,
  LinkIcon,
  MoreHorizontalIcon,
  Trash2Icon,
} from "lucide-react"
import * as React from "react"

import { useCopy } from "@/components/console/copy-id"
import { useReturnFocus } from "@/components/console/return-focus"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ApiError } from "@/lib/api/errors"
import { api } from "@/lib/api/http"
import { forget } from "@/lib/api/queries"
import { count } from "@/lib/format"

/** Why a session was not deleted. The server refuses (409) while one
 * of its runs is queued, running or waiting on a person. */
export function deleteError(e: unknown): string {
  if (e instanceof ApiError && e.status === 409 && e.code === "run_active")
    return "A run of this session is still going. Cancel it first, then delete."
  return e instanceof Error ? e.message : String(e)
}

/** SessionMenu: the session's "…" menu. Export (JSON lines: every turn's
 * run, then the session's state log), copy link, delete (confirmed). */
export function SessionMenu({
  sessionId,
  turns,
}: {
  sessionId: string
  /** How many turns a delete removes, for the confirmation. */
  turns?: number
}) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const router = useRouter()
  const [copied, copy] = useCopy()
  const [deleting, setDeleting] = React.useState(false)
  const menu = useReturnFocus()
  const del = useMutation({
    mutationFn: () => api.deleteSession(sessionId),
    onSuccess: () => {
      forget(qc, { session: sessionId })
      void navigate({ to: "/sessions" })
    },
  })
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            ref={menu.ref}
            size="icon-sm"
            variant="ghost"
            aria-label="More actions"
          >
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-52">
          <DropdownMenuItem asChild>
            <a href={api.sessionExportUrl(sessionId)} download>
              <DownloadIcon />
              Export (JSONL)
            </a>
          </DropdownMenuItem>
          <DropdownMenuItem
            onSelect={(e) => {
              e.preventDefault()
              // The address of this page (with the open turn), under
              // whatever prefix the Console is mounted at.
              const { href } = router.state.location
              copy(
                new URL(href.replace(/^\//, ""), document.baseURI).toString()
              )
            }}
          >
            {copied ? <CheckIcon /> : <LinkIcon />}
            {copied ? "Link copied" : "Copy link"}
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => setDeleting(true)}
          >
            <Trash2Icon />
            Delete session…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent
          className="sm:max-w-md"
          onCloseAutoFocus={menu.onCloseAutoFocus}
        >
          <DialogHeader>
            <DialogTitle>Delete this session?</DialogTitle>
            <DialogDescription>
              {turns
                ? `Its ${count(turns)} ${turns === 1 ? "turn is" : "turns are"} removed from this server with their runs, messages and logs.`
                : "Its turns are removed from this server with their runs, messages and logs."}{" "}
              Your app&apos;s own copy of the conversation is not touched. This
              cannot be undone.
            </DialogDescription>
          </DialogHeader>
          {del.error && (
            <p role="alert" className="text-xs text-danger-foreground">
              {deleteError(del.error)}
            </p>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setDeleting(false)}>
              Keep it
            </Button>
            <Button
              variant="destructive"
              disabled={del.isPending}
              onClick={() => del.mutate()}
            >
              {del.isPending ? "Deleting…" : "Delete session"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
