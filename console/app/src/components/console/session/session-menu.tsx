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
import { api } from "@/lib/api/http"
import { count } from "@/lib/format"

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
  const del = useMutation({
    mutationFn: () => api.deleteSession(sessionId),
    onSuccess: () => {
      qc.removeQueries({ queryKey: ["session", sessionId] })
      for (const key of [["sessions"], ["runs"], ["stats"], ["approvals"]])
        void qc.invalidateQueries({ queryKey: key })
      void navigate({ to: "/sessions" })
    },
  })
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="icon-sm" variant="ghost" aria-label="More actions">
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
        <DialogContent className="sm:max-w-md">
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
              {del.error instanceof Error
                ? del.error.message
                : String(del.error)}
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
