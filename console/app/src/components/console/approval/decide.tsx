import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ChevronDownIcon } from "lucide-react"
import * as React from "react"

import { JsonView } from "@/components/console/json-view"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Textarea } from "@/components/ui/textarea"
import { afterDecision, decide, decisionError } from "@/lib/api/approvals"
import type { DecisionInput } from "@/lib/api/approvals"
import type { Approval } from "@/lib/api/types"
import { approvalState } from "@/lib/approval-view"
import { cn } from "@/lib/utils"

// DecideForm: the one place a person decides a pending approval. The
// exact arguments, then Approve, Deny (with an optional reason) and,
// under More, "Answer for the tool" (the result the agent reads instead
// of running it). The decision is bound to the arguments shown: it
// carries the approval's own args_sha256, and a refusal (the arguments
// changed, someone else decided) is said in a sentence.
//
// Self-contained: it sends the decision and refreshes every run,
// session and approvals query. Used by the run page's callout and by
// the approvals inbox.
//
//   <DecideForm approval={a} onDecided={(a) => …} />

type Mode = "idle" | "deny" | "resolve"

export function DecideForm({
  approval,
  onDecided,
  showArgs = true,
  className,
}: {
  /** A pending approval, as the server returned it. */
  approval: Approval
  /** After the server accepted the decision; `resumed_by` is set on the
   * answer when the run continues. */
  onDecided?: (approval: Approval) => void
  /** false when the caller already shows the arguments. */
  showArgs?: boolean
  className?: string
}) {
  const qc = useQueryClient()
  const [mode, setMode] = React.useState<Mode>("idle")
  const [text, setText] = React.useState("")
  const m = useMutation({
    mutationFn: (d: DecisionInput) => decide(approval, d),
    onSuccess: (a) => onDecided?.(a),
    onSettled: () => {
      for (const key of afterDecision)
        void qc.invalidateQueries({ queryKey: key })
    },
  })
  const busy = m.isPending
  // Decided: say so at once. The lists that show this approval refetch
  // and replace the form; until they do there is nothing left to press.
  if (m.isSuccess)
    return (
      <p role="status" className={cn("text-sm", className)}>
        <span className="font-medium">{approvalState(m.data).label}.</span>{" "}
        <span className="text-muted-foreground">
          {m.data.resumed_by
            ? "The run continues."
            : "The run continues once its other calls are decided."}
        </span>
      </p>
    )
  return (
    <div className={cn("flex min-w-0 flex-col gap-2", className)}>
      {showArgs && (
        <JsonView value={approval.args} depth={3} className="bg-card" />
      )}
      {mode !== "idle" && (
        <label className="flex flex-col gap-1 text-xs text-muted-foreground">
          {mode === "deny"
            ? "Reason (optional; the agent reads it)"
            : "The result the agent reads instead of running the tool"}
          <Textarea
            autoFocus
            value={text}
            onChange={(e) => setText(e.target.value)}
            rows={mode === "deny" ? 2 : 4}
            className={cn(
              "bg-card text-foreground",
              mode === "resolve" && "font-mono text-xs"
            )}
            placeholder={
              mode === "deny"
                ? "Not eligible: outside the 30-day window"
                : '{"refund_id": "manual-1", "state": "settled"}'
            }
          />
        </label>
      )}
      <div className="flex flex-wrap items-center gap-2">
        {mode === "idle" ? (
          <>
            <Button
              size="sm"
              disabled={busy}
              onClick={() => m.mutate({ decision: "approve" })}
            >
              Approve
            </Button>
            <Button
              size="sm"
              variant="secondary"
              disabled={busy}
              onClick={() => {
                setText("")
                setMode("deny")
              }}
            >
              Deny…
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button size="sm" variant="ghost" disabled={busy}>
                  More
                  <ChevronDownIcon />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start">
                <DropdownMenuItem
                  onSelect={() => {
                    setText("")
                    setMode("resolve")
                  }}
                >
                  Answer for the tool…
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        ) : (
          <>
            <Button
              size="sm"
              variant={mode === "deny" ? "destructive" : "default"}
              disabled={busy || (mode === "resolve" && text.trim() === "")}
              onClick={() =>
                m.mutate(
                  mode === "deny"
                    ? { decision: "deny", reason: text.trim() }
                    : { decision: "resolve", content: text }
                )
              }
            >
              {mode === "deny" ? "Deny" : "Send the answer"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              onClick={() => setMode("idle")}
            >
              Back
            </Button>
          </>
        )}
        <span
          className="ml-auto truncate font-mono text-xs text-subtle-foreground"
          title={`The decision applies to exactly these arguments (sha256 ${approval.args_sha256}). If they change first, it is refused.`}
        >
          args {approval.args_sha256.slice(0, 10)}
        </span>
      </div>
      {m.error && (
        <p role="alert" className="text-xs text-danger-foreground">
          {decisionError(m.error)}
        </p>
      )}
    </div>
  )
}
