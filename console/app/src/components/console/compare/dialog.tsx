import { useQuery } from "@tanstack/react-query"
import { Link, useNavigate } from "@tanstack/react-router"
import * as React from "react"

import {
  EmptyState,
  ErrorState,
  LoadingRows,
} from "@/components/console/empty-state"
import { RelativeTime } from "@/components/console/relative-time"
import { Status } from "@/components/console/status"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { q } from "@/lib/api/queries"
import { shortId } from "@/lib/format"
import { rowCause, rowStatus, rowTimes } from "@/lib/runs"

// "Compare with…": choose the other run. The recent runs of the same
// agent, newest first, or any run by its id.

/** runIdOf reads a run id out of what was pasted: the id itself, or a
 * link to a run page. */
export function runIdOf(text: string): string | undefined {
  const t = text.trim()
  if (!t) return undefined
  const m = /\/runs\/([^/?#\s]+)/.exec(t)
  if (m) return decodeURIComponent(m[1])
  return /\s/.test(t) ? undefined : t
}

export function CompareDialog({
  runId,
  open,
  onOpenChange,
  onCloseAutoFocus,
}: {
  runId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Where the focus goes on close (useReturnFocus). */
  onCloseAutoFocus?: (e: Event) => void
}) {
  const navigate = useNavigate()
  const [text, setText] = React.useState("")
  const run = useQuery({ ...q.run(runId), enabled: open })
  const agent = run.data?.agent
  const runs = useQuery({
    ...q.runsPage({ agent, limit: 20 }),
    enabled: open && !!agent,
  })
  // The run itself is not a choice: neither its own row nor the row of
  // the chain it is part of.
  const own = new Set([
    runId,
    ...(run.data?.chain?.runs.map((r) => r.id) ?? []),
  ])
  const rows = runs.data?.runs.filter(
    (r) => !own.has(r.id) && !r.chain?.runs.some((c) => own.has(c.id))
  )
  const pasted = runIdOf(text)
  const error = run.error ?? runs.error

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="sm:max-w-xl"
        onCloseAutoFocus={onCloseAutoFocus}
      >
        <DialogHeader>
          <DialogTitle>Compare with…</DialogTitle>
          <DialogDescription className="sr-only">
            Choose the run to show beside this one, step by step.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            if (!pasted || pasted === runId) return
            onOpenChange(false)
            void navigate({
              to: "/runs/$runId/compare",
              params: { runId },
              search: { with: pasted },
            })
          }}
        >
          <Input
            value={text}
            onChange={(e) => setText(e.target.value)}
            aria-label="Run id"
            placeholder="Paste a run id or a link to a run"
            className="font-mono placeholder:font-sans"
            spellCheck={false}
            autoComplete="off"
          />
          <Button
            type="submit"
            variant="secondary"
            disabled={!pasted || pasted === runId}
          >
            Compare
          </Button>
        </form>
        <div className="flex min-w-0 flex-col gap-2">
          <h3 className="table-head">
            {agent ? `Recent runs of ${agent}` : "Recent runs"}
          </h3>
          <div className="max-h-80 overflow-auto rounded-alert border bg-card">
            {error ? (
              <ErrorState error={error} what="The runs" />
            ) : !rows ? (
              <LoadingRows rows={6} />
            ) : rows.length === 0 ? (
              <EmptyState title="No other run of this agent">
                Paste the id of any run above to compare with it.
              </EmptyState>
            ) : (
              <ul>
                {rows.map((r) => (
                  <li key={r.id} className="border-b last:border-b-0">
                    <Link
                      to="/runs/$runId/compare"
                      params={{ runId }}
                      search={{ with: r.id }}
                      onClick={() => onOpenChange(false)}
                      className="grid h-8 grid-cols-[6.5rem_minmax(0,1fr)_auto_auto] items-center gap-3 px-3 outline-none hover:bg-muted focus-visible:bg-muted"
                    >
                      <Status state={rowStatus(r)} cause={rowCause(r)} />
                      <span className="truncate text-sm">
                        {r.prompt_preview || (
                          <span className="text-subtle-foreground">
                            No input recorded
                          </span>
                        )}
                      </span>
                      <RelativeTime
                        time={rowTimes(r).started}
                        className="text-xs whitespace-nowrap text-muted-foreground"
                      />
                      <span className="font-mono text-xs text-subtle-foreground">
                        {shortId(r.id, 6)}
                      </span>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
