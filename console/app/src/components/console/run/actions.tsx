import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate, useRouter } from "@tanstack/react-router"
import {
  CheckIcon,
  DownloadIcon,
  ExternalLinkIcon,
  GitCompareArrowsIcon,
  LinkIcon,
  MoreHorizontalIcon,
  SendIcon,
  SquareIcon,
  Trash2Icon,
} from "lucide-react"
import * as React from "react"

import { CompareDialog } from "@/components/console/compare/dialog"
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
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Textarea } from "@/components/ui/textarea"
import { ApiError, actionError } from "@/lib/api/errors"
import { api } from "@/lib/api/http"
import type { ExportFormat } from "@/lib/api/http"
import { afterRunWrite, forget } from "@/lib/api/queries"
import type { DisplayStatus } from "@/lib/api/status"

// The run page's actions: Cancel while the run is queued, running or
// waiting on a person; "Send message" while it runs; and the "…" menu
// (export, copy link, open in the app, delete).

const FORMATS: [ExportFormat, string][] = [
  ["json", "JSON"],
  ["jsonl", "JSONL"],
  ["otlp", "OTLP"],
]

const message = actionError

/** appUrl: the app page a run names, only when it is a web address
 * (http or https). Anything else (javascript:, data:, a relative path)
 * is not a link the Console opens. */
export function appUrl(v: unknown): string | undefined {
  if (typeof v !== "string" || !v) return undefined
  try {
    const u = new URL(v)
    return u.protocol === "http:" || u.protocol === "https:"
      ? u.toString()
      : undefined
  } catch {
    return undefined
  }
}

/** The statuses a run can be cancelled in. */
const CANCELLABLE: readonly DisplayStatus[] = ["queued", "running", "waiting"]

export function RunActions({
  runId,
  status,
  hasChildren,
  pageUrl,
  chainIds,
  sessionId,
}: {
  /** The newest run of the chain: the one that is going. */
  runId: string
  /** The chain's status as shown. */
  status: DisplayStatus
  hasChildren: boolean
  /** The app page the run was started from, when the run carries one
   * (only an http or https address is offered: appUrl). */
  pageUrl?: string
  /** Every run of the chain (a delete removes them all). */
  chainIds?: readonly string[]
  /** The session the run is a turn of. */
  sessionId?: string
}) {
  const running = status === "running"
  const cancellable = CANCELLABLE.includes(status)
  const link = appUrl(pageUrl)
  const menu = useReturnFocus()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const router = useRouter()
  const [copied, copy] = useCopy()
  const [tree, setTree] = React.useState(false)
  const [steering, setSteering] = React.useState(false)
  const [deleting, setDeleting] = React.useState(false)
  const [comparing, setComparing] = React.useState(false)
  const [text, setText] = React.useState("")
  const refresh = () => {
    for (const key of afterRunWrite(runId))
      void qc.invalidateQueries({ queryKey: key })
  }
  const cancel = useMutation({
    mutationFn: () => api.cancel(runId, "cancelled from the Console"),
    onSettled: refresh,
  })
  // Set synchronously: isPending only shows on the next render, after
  // a second key event may already have arrived.
  const sending = React.useRef(false)
  const steer = useMutation({
    mutationFn: (t: string) => api.steer(runId, t),
    onSettled: () => {
      sending.current = false
    },
    onSuccess: () => {
      setSteering(false)
      setText("")
      refresh()
    },
  })
  // One send at a time: the button and Ctrl/Cmd+Enter both go through
  // here, and a key held down repeats.
  const send = () => {
    const t = text.trim()
    if (!t || sending.current) return
    sending.current = true
    steer.mutate(t)
  }
  const del = useMutation({
    mutationFn: () => api.deleteRun(runId),
    onSuccess: () => {
      forget(qc, {
        runs: [...new Set([runId, ...(chainIds ?? [])])],
        session: sessionId,
      })
      void navigate({ to: "/runs" })
    },
  })

  return (
    <>
      {cancellable && cancel.error && (
        <span
          role="alert"
          className="max-w-64 truncate text-xs text-danger-foreground"
          title={message(cancel.error)}
        >
          Not cancelled: {message(cancel.error)}
        </span>
      )}
      {running && (
        <Button size="sm" variant="secondary" onClick={() => setSteering(true)}>
          <SendIcon />
          Send message
        </Button>
      )}
      {cancellable && (
        <Button
          size="sm"
          variant="secondary"
          disabled={cancel.isPending}
          onClick={() => cancel.mutate()}
          title={
            status === "queued"
              ? "Take this run off the queue"
              : status === "waiting"
                ? "Stop this run instead of waiting for a decision"
                : "Stop this run"
          }
        >
          <SquareIcon />
          {cancel.isPending ? "Cancelling…" : "Cancel"}
        </Button>
      )}
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
          {link && (
            <DropdownMenuItem asChild>
              <a href={link} target="_blank" rel="noreferrer noopener">
                <ExternalLinkIcon />
                Open in app
              </a>
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onSelect={() => setComparing(true)}>
            <GitCompareArrowsIcon />
            Compare with…
          </DropdownMenuItem>
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
              send()
            }}
          >
            <Textarea
              autoFocus
              rows={4}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey)) return
                e.preventDefault()
                send()
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

      <CompareDialog
        runId={runId}
        open={comparing}
        onOpenChange={setComparing}
        onCloseAutoFocus={menu.onCloseAutoFocus}
      />

      <Dialog open={deleting} onOpenChange={setDeleting}>
        <DialogContent
          className="sm:max-w-md"
          onCloseAutoFocus={menu.onCloseAutoFocus}
        >
          <DialogHeader>
            <DialogTitle>Delete this run?</DialogTitle>
            <DialogDescription>
              The whole run is removed from this server: every part of it (a
              wait and what continued it), its spans, messages, logs and
              approvals. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          {del.error && (
            <p role="alert" className="text-xs text-danger-foreground">
              {del.error instanceof ApiError && del.error.status === 409
                ? "This run is still going (queued, running or waiting on a person). Cancel it first, then delete it."
                : message(del.error)}
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
