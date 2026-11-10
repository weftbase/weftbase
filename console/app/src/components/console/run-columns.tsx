import type { Column } from "@/components/console/data-table"
import { RelativeTime, useNow } from "@/components/console/relative-time"
import { StatusDot } from "@/components/console/status"
import type { RunRow } from "@/lib/api/types"
import {
  between,
  cost,
  count,
  duration,
  tokens,
  totalTokens,
} from "@/lib/format"
import { displayStatus } from "@/lib/api/status"

function Duration({ run }: { run: RunRow }) {
  const now = useNow()
  // A run still going is measured to now; one that stopped reporting
  // without an end has no duration.
  if (!run.finished && run.status !== "running") return <>—</>
  return <>{duration(between(run.started, run.finished, now))}</>
}

/** The Runs list's columns (CONSOLE-UX, Runs): status dot, agent, input
 * preview, model, steps, duration, tokens, cost, started. */
export const RUN_COLUMNS: Column<RunRow>[] = [
  {
    key: "status",
    header: <span className="sr-only">Status</span>,
    width: "28px",
    cell: (r) => <StatusDot state={displayStatus(r)} />,
  },
  {
    key: "agent",
    header: "Agent",
    width: "minmax(120px, 180px)",
    cell: (r) => <span className="font-medium">{r.agent || "—"}</span>,
  },
  {
    key: "input",
    header: "Input",
    width: "minmax(180px, 1fr)",
    muted: true,
    cell: (r) =>
      r.status === "failed" && r.err ? (
        <span>
          {r.prompt_preview}
          <span className="text-danger-foreground">
            {r.prompt_preview ? " · " : ""}
            {r.err}
          </span>
        </span>
      ) : (
        (r.prompt_preview ?? "—")
      ),
  },
  {
    key: "model",
    header: "Model",
    width: "minmax(100px, 160px)",
    muted: true,
    cell: (r) => r.model.name || "—",
  },
  {
    key: "steps",
    header: "Steps",
    width: "56px",
    align: "right",
    mono: true,
    cell: (r) => count(r.steps),
  },
  {
    key: "duration",
    header: "Duration",
    width: "80px",
    align: "right",
    mono: true,
    cell: (r) => <Duration run={r} />,
  },
  {
    key: "tokens",
    header: "Tokens",
    width: "72px",
    align: "right",
    mono: true,
    cell: (r) => tokens(totalTokens(r.usage)),
  },
  {
    key: "cost",
    header: "Cost",
    width: "80px",
    align: "right",
    mono: true,
    cell: (r) => <span title={r.cost_reason}>{cost(r)}</span>,
  },
  {
    key: "started",
    header: "Started",
    width: "80px",
    align: "right",
    muted: true,
    cell: (r) => <RelativeTime time={r.started} />,
  },
]

/** The same row, shorter: for a list inside a section (the overview). */
export const RUN_COLUMNS_SHORT: Column<RunRow>[] = RUN_COLUMNS.filter((c) =>
  ["status", "agent", "input", "duration", "started"].includes(c.key)
)
