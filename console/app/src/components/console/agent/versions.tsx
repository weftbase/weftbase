import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Link } from "@tanstack/react-router"
import { ArrowRightIcon } from "lucide-react"
import * as React from "react"

import { driftNote } from "@/components/console/agent/inventory"
import { CopyId } from "@/components/console/copy-id"
import { DataTable } from "@/components/console/data-table"
import type { Column } from "@/components/console/data-table"
import { RelativeTime } from "@/components/console/relative-time"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { api } from "@/lib/api/http"
import type { AgentDetail, VersionDetail } from "@/lib/api/types"
import { changeLines, moveError } from "@/lib/agents"

export function LabelChip({ label }: { label: string }) {
  return (
    <span className="inline-flex h-[18px] flex-none items-center rounded-sm bg-muted px-1.5 text-[11px] font-medium">
      {label}
    </span>
  )
}

const NEW = "__new__"
const LABEL_RE = /^[a-z0-9][a-z0-9._-]{0,62}$/

/** Move label: point a label at this version (promote or roll back).
 * The request names what the label holds now (`expected`), so if
 * someone moved it meanwhile the server refuses and nothing changes. */
export function MoveLabel({
  agent,
  version,
  onClose,
}: {
  agent: AgentDetail
  /** The version the label will point to. */
  version: VersionDetail
  onClose: () => void
}) {
  const qc = useQueryClient()
  const labels = Object.keys(agent.labels).sort()
  const movable = labels.filter((l) => agent.labels[l] !== version.id)
  const [choice, setChoice] = React.useState(movable.at(0) ?? NEW)
  const [name, setName] = React.useState("")
  const [reason, setReason] = React.useState("")
  const label = choice === NEW ? name.trim() : choice
  const current = choice === NEW ? undefined : agent.by_label[label]
  const valid = LABEL_RE.test(label) && agent.labels[label] !== version.id
  const m = useMutation({
    mutationFn: () =>
      api.promote(agent.name, label, {
        version: version.id,
        expected: agent.labels[label] ?? "",
        reason: reason.trim() || undefined,
      }),
    onSuccess: onClose,
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["agent", agent.name] })
      void qc.invalidateQueries({ queryKey: ["agents"] })
      void qc.invalidateQueries({ queryKey: ["tools"] })
    },
  })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            Move a label to {agent.name} v{version.number}
          </DialogTitle>
          <DialogDescription>
            New runs that ask for the label start on this version. Runs already
            going are not touched.
          </DialogDescription>
        </DialogHeader>
        <form
          id="move-label"
          onSubmit={(e) => {
            e.preventDefault()
            if (valid) m.mutate()
          }}
          className="flex flex-col gap-3"
        >
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            Label
            <Select value={choice} onValueChange={setChoice}>
              <SelectTrigger aria-label="Label" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent position="popper" align="start">
                {labels.map((l) => (
                  <SelectItem
                    key={l}
                    value={l}
                    disabled={agent.labels[l] === version.id}
                  >
                    {l}
                    {agent.labels[l] === version.id
                      ? " (already here)"
                      : ` (now v${agent.by_label[l]?.number ?? "?"})`}
                  </SelectItem>
                ))}
                <SelectItem value={NEW}>A new label…</SelectItem>
              </SelectContent>
            </Select>
          </label>
          {choice === NEW && (
            <label className="flex flex-col gap-1 text-xs text-muted-foreground">
              Name of the new label
              <Input
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="canary"
                spellCheck={false}
                aria-invalid={name.trim() !== "" && !LABEL_RE.test(name.trim())}
                className="font-mono text-xs text-foreground"
              />
            </label>
          )}
          <p className="flex h-8 items-center gap-2 rounded-input border bg-surface px-2 text-sm">
            <LabelChip label={label || "label"} />
            <span className="font-mono text-xs text-muted-foreground">
              {current ? `v${current.number}` : "not set"}
            </span>
            <ArrowRightIcon
              aria-hidden
              className="size-3.5 text-subtle-foreground"
            />
            <span className="font-mono text-xs font-medium">
              v{version.number}
            </span>
            {current && (
              <span className="ml-auto text-xs text-muted-foreground">
                {current.number > version.number ? "a rollback" : "a promotion"}
              </span>
            )}
          </p>
          <label className="flex flex-col gap-1 text-xs text-muted-foreground">
            Reason (optional; kept with the change)
            <Input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Passed the staging checks"
              className="text-foreground"
            />
          </label>
          {m.error && (
            <p role="alert" className="text-xs text-danger-foreground">
              {moveError(m.error, label)}
            </p>
          )}
        </form>
        <DialogFooter>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="move-label"
            disabled={!valid || m.isPending}
          >
            {m.isPending
              ? "Moving…"
              : `Move ${label || "label"} to v${version.number}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** Versions, newest first: labels, what changed against the previous
 * one, and Move label. */
export function Versions({
  agent,
  shown,
}: {
  agent: AgentDetail
  /** The version whose capabilities are shown above. */
  shown: string | undefined
}) {
  const [moving, setMoving] = React.useState<VersionDetail | null>(null)
  const columns: Column<VersionDetail>[] = [
    {
      key: "number",
      header: "Version",
      width: "72px",
      cell: (v) => (
        <Link
          to="/agents/$name"
          params={{ name: agent.name }}
          search={{ v: v.id === agent.newest?.id ? undefined : v.number }}
          replace
          title="Show this version's capabilities"
          className="font-mono text-xs font-medium underline-offset-4 hover:underline"
        >
          v{v.number}
        </Link>
      ),
    },
    {
      key: "labels",
      header: "Labels",
      width: "minmax(96px, 180px)",
      cell: (v) => (
        <span className="flex items-center gap-1">
          {(v.labels ?? []).map((l) => (
            <LabelChip key={l} label={l} />
          ))}
          {(v.labels ?? []).length === 0 && (
            <span className="text-subtle-foreground">—</span>
          )}
        </span>
      ),
    },
    {
      key: "changes",
      header: "What changed",
      width: "minmax(200px, 1fr)",
      muted: true,
      cell: (v) => {
        const lines = changeLines(v.changes)
        const drift = driftNote(v.manifest_drift)
        return (
          <span className="flex min-w-0 items-center gap-1.5">
            <span className="min-w-0 truncate" title={lines.join("; ")}>
              {lines.join("; ")}
            </span>
            {drift && (
              <span
                className="flex-none rounded-sm bg-warning-muted px-1 text-[11px] leading-[18px] font-medium text-warning-foreground"
                title={drift}
              >
                differs
              </span>
            )}
          </span>
        )
      },
    },
    {
      key: "placement",
      hideBelow: 760,
      header: "Placement",
      width: "88px",
      muted: true,
      cell: (v) => v.placement || "—",
    },
    {
      key: "id",
      hideBelow: 680,
      header: "Id",
      width: "112px",
      cell: (v) => <CopyId id={v.id} label="version id" />,
    },
    {
      key: "created",
      hideBelow: 560,
      header: "Registered",
      width: "88px",
      align: "right",
      muted: true,
      cell: (v) => <RelativeTime time={v.created_at} />,
    },
    {
      key: "move",
      header: <span className="sr-only">Move a label</span>,
      width: "96px",
      align: "right",
      cell: (v) => (
        <Button size="sm" variant="ghost" onClick={() => setMoving(v)}>
          Move label
        </Button>
      ),
    },
  ]
  return (
    <>
      <DataTable
        label="Versions"
        bordered
        stickyHeader={false}
        columns={columns}
        rows={agent.version_list}
        rowKey={(v) => v.id}
        selectedKey={shown}
      />
      {moving && (
        <MoveLabel
          agent={agent}
          version={moving}
          onClose={() => setMoving(null)}
        />
      )}
    </>
  )
}
