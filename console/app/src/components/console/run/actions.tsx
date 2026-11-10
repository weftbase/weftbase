import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate, useRouter } from "@tanstack/react-router"
import {
  CheckIcon,
  DownloadIcon,
  ExternalLinkIcon,
  LinkIcon,
  MoreHorizontalIcon,
  SendIcon,
  SquareIcon,
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
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api/http"
import type { ExportFormat } from "@/lib/api/http"
import { afterRunWrite } from "@/lib/api/queries"

// The run page's actions: Cancel and "Send message" while it runs, and
// the "…" menu (export, copy link, open in the app, delete).

const FORMATS: [ExportFormat, string][] = [
  ["json", "JSON"],
  ["jsonl", "JSONL"],
  ["otlp", "OTLP"],
]

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

export function RunActions({
  runId,
  running,
  hasChildren,
  pageUrl,
}: {
  /** The newest run of the chain: the one that is going. */
  runId: string
  running: boolean
  hasChildren: boolean
  /** The app page the run was started from, when the run carries one. */
  pageUrl?: string
}) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const router = useRouter()
  const [copied, copy] = useCopy()
  const [tree, setTree] = React.useState(false)
  const [steering, setSteering] = React.useState(false)
  const [deleting, setDeleting] = React.useState(false)
  const [text, setText] = React.useState("")
  const refresh = () => {
    for (const key of afterRunWrite(runId))
      void qc.invalidateQueries({ queryKey: key })
  }
  const cancel = useMutation({
    mutationFn: () => api.cancel(runId, "cancelled from the Console"),
    onSettled: refresh,
  })
  const steer = useMutation({
    mutationFn: (t: string) => api.steer(runId, t),
    onSuccess: () => {
      setSteering(false)
      setText("")
      refresh()
    },
  })
  const del = useMutation({
    mutationFn: () => api.deleteRun(runId),
    onSuccess: () => {
      qc.removeQueries({ queryKey: ["run", runId] })
      void qc.invalidateQueries({ queryKey: ["runs"] })
      void navigate({ to: "/runs" })
    },
  })

  return (
    <>
      {running && (
        <>
          <Button
            size="sm"
            variant="secondary"
            onClick={() => setSteering(true)}
          >
            <SendIcon />
            Send message
          </Button>
          <Button
            size="sm"
            variant="secondary"
            disabled={cancel.isPending}
            onClick={() => cancel.mutate()}
            title={cancel.error ? message(cancel.error) : "Stop this run"}
          >
            <SquareIcon />
            {cancel.isPending ? "Cancelling…" : "Cancel"}
          </Button>
        </>
      )}
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="icon-sm" variant="ghost" aria-label="More actions">
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-52">
          <DropdownMenuLabel>Export</DropdownMenuLabel>
          {FORMATS.map(([f, label]) => (
            <DropdownMenuItem key={f} asChild>
              <a href={api.exportUrl(runId, f, tree)} download>
                <DownloadIcon />
                Download {label}
              </a>
            </DropdownMenuItem>
          ))}
          {hasChildren && (
            <DropdownMenuCheckboxItem
              checked={tree}
              onCheckedChange={(v) => setTree(v === true)}
              onSelect={(e) => e.preventDefault()}
            >
              Include child runs
            </DropdownMenuCheckboxItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onSelect={(e) => {
              e.preventDefault()
              // The address of this page (with its selection), under
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
          {pageUrl && (
            <DropdownMenuItem asChild>
              <a href={pageUrl} target="_blank" rel="noreferrer noopener">
                <ExternalLinkIcon />
                Open in app
              </a>
            </DropdownMenuItem>
          )}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onSelect={() => setDeleting(true)}
          >
            <Trash2Icon />
            Delete run…
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={steering} onOpenChange={setSteering}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Send a message to the running agent</DialogTitle>
            <DialogDescription>
              The agent reads it before its next model call. It does not restart
              the run.
            </DialogDescription>
          </DialogHeader>
          <form
            className="flex flex-col gap-3"
            onSubmit={(e) => {
              e.preventDefault()
              if (text.trim()) steer.mutate(text.trim())
            }}
          >
            <Textarea
              autoFocus
              rows={4}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (
                  e.key === "Enter" &&
                  (e.metaKey || e.ctrlKey) &&
                  text.trim()
                )
                  steer.mutate(text.trim())
              }}
              aria-label="Message"
              placeholder="Also check whether the order was already refunded."
            />
            {steer.error && (
              <p role="alert" className="text-xs text-danger-foreground">
                {message(steer.error)}
              </p>
            )}
            <DialogFooter>
              <Button
                type="button"
                variant="ghost"
                onClick={() => setSteering(false)}
              >
                Close
              </Button>
              <Button type="submit" disabled={!text.trim() || steer.isPending}>
                {steer.isPending ? "Sending…" : "Send"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Delete this run?</DialogTitle>
            <DialogDescription>
              Its spans, messages and logs are removed from this server. This
              cannot be undone.
            </DialogDescription>
          </DialogHeader>
          {del.error && (
            <p role="alert" className="text-xs text-danger-foreground">
              {message(del.error)}
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
              {del.isPending ? "Deleting…" : "Delete run"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
