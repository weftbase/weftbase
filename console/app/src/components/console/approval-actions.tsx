import { useMutation, useQueryClient } from "@tanstack/react-query"
import { CheckIcon, XIcon } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { api } from "@/lib/api/client"
import { afterDecisionKeys } from "@/lib/api/queries"
import type { Approval, Env } from "@/lib/api/types"

// Approve and Deny for one approval. A decision binds to the arguments
// the person was shown (args_sha256). Deny asks for a reason.
export function ApprovalActions({
  approval,
  env,
}: {
  approval: Approval
  env: Env
}) {
  const qc = useQueryClient()
  const [denyOpen, setDenyOpen] = React.useState(false)
  const [reason, setReason] = React.useState("")
  const decide = useMutation({
    mutationFn: (d: { approve: boolean; reason?: string }) =>
      api.decide(env, approval.id, { ...d, args_sha256: approval.args_sha256 }),
    onSuccess: (a) => {
      for (const key of afterDecisionKeys)
        void qc.invalidateQueries({ queryKey: [...key] })
      toast.success(
        a.status === "approved"
          ? `Approved ${a.tool} for ${a.run_id}`
          : `Denied ${a.tool} for ${a.run_id}`
      )
      setDenyOpen(false)
    },
    onError: (e) => toast.error(e.message),
  })

  if (approval.status !== "pending") return null
  return (
    <div className="flex flex-wrap gap-2.5">
      <Button
        size="sm"
        disabled={decide.isPending}
        onClick={() => decide.mutate({ approve: true })}
      >
        <CheckIcon />
        Approve
      </Button>
      <Button
        size="sm"
        variant="secondary"
        disabled={decide.isPending}
        onClick={() => setDenyOpen(true)}
      >
        <XIcon />
        Deny
      </Button>
      <Dialog open={denyOpen} onOpenChange={setDenyOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Deny {approval.tool}</DialogTitle>
            <DialogDescription>
              The run gets the reason as the tool result and decides what to do
              next.
            </DialogDescription>
          </DialogHeader>
          <form
            className="flex flex-col gap-1.5"
            onSubmit={(e) => {
              e.preventDefault()
              decide.mutate({ approve: false, reason: reason.trim() })
            }}
          >
            <Label htmlFor="deny-reason">Reason</Label>
            <Textarea
              id="deny-reason"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Outside the 30-day refund window."
              required
            />
            <p className="text-xs text-subtle-foreground">
              The agent and the audit log both see this.
            </p>
            <DialogFooter className="mt-4">
              <Button
                type="button"
                size="sm"
                variant="secondary"
                onClick={() => setDenyOpen(false)}
              >
                Cancel
              </Button>
              <Button
                type="submit"
                size="sm"
                variant="destructive"
                disabled={!reason.trim() || decide.isPending}
              >
                Deny
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}

/** The amber callout that ends a waiting run's step list (Alert README). */
export function ApprovalCallout({
  approval,
  env,
}: {
  approval: Approval
  env: Env
}) {
  if (approval.status !== "pending") return null
  return (
    <div className="flex flex-col gap-3 rounded-alert bg-warning-muted p-3.5">
      <div className="text-sm text-foreground">
        <span className="font-semibold">{approval.tool}</span> needs a person to
        approve it.
      </div>
      <ApprovalActions approval={approval} env={env} />
    </div>
  )
}
