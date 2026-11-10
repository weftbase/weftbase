// The shared primitives render what they are given, plainly.
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import { ApprovalOutcome } from "@/components/console/approval/outcome"
import { DataTable } from "@/components/console/data-table"
import { RUN_COLUMNS } from "@/components/console/run-columns"
import { TooltipProvider } from "@/components/ui/tooltip"
import { JsonView } from "@/components/console/json-view"
import { RelativeTime } from "@/components/console/relative-time"
import { StatStrip } from "@/components/console/stat-strip"
import { StatusBadge } from "@/components/console/status"
import type { RunRow, RunsPage } from "@/lib/api/types"
import { displayStatus } from "@/lib/api/status"

import runs from "./fixtures/runs.json"
import runsOutcomes from "./fixtures/runs-outcomes.json"

const rows = runs.runs as unknown as RunRow[]

afterEach(cleanup)

describe("DataTable", () => {
  const table = () =>
    render(
      <DataTable
        label="Runs"
        rows={rows}
        rowKey={(r) => r.id}
        columns={[
          { key: "id", header: "Run", width: "1fr", cell: (r) => r.id },
          { key: "agent", header: "Agent", width: "1fr", cell: (r) => r.agent },
        ]}
      />
    )

  it("renders a row per item under its headers", () => {
    table()
    expect(screen.getByRole("table", { name: "Runs" })).toBeTruthy()
    expect(
      screen.getAllByRole("columnheader").map((h) => h.textContent)
    ).toEqual(["Run", "Agent"])
    expect(screen.getAllByRole("row")).toHaveLength(rows.length + 1)
    expect(screen.getByText("r_fail")).toBeTruthy()
  })

  it("moves between rows with the arrow keys, j/k, Home and End", () => {
    table()
    const body = screen.getAllByRole("row").slice(1)
    body[0].focus()
    fireEvent.keyDown(body[0], { key: "ArrowDown" })
    expect(document.activeElement).toBe(body[1])
    fireEvent.keyDown(body[1], { key: "j" })
    expect(document.activeElement).toBe(body[2])
    fireEvent.keyDown(body[2], { key: "k" })
    expect(document.activeElement).toBe(body[1])
    fireEvent.keyDown(body[1], { key: "End" })
    expect(document.activeElement).toBe(body[3])
    fireEvent.keyDown(body[3], { key: "ArrowDown" })
    expect(document.activeElement).toBe(body[3])
    fireEvent.keyDown(body[3], { key: "Home" })
    expect(document.activeElement).toBe(body[0])
  })
})

describe("StatStrip", () => {
  it("is one list of equal cells: a label over a value", () => {
    const { container } = render(
      <StatStrip
        stats={[
          { label: "Runs", value: "10" },
          { label: "Cost", value: "unknown", hint: "1 model unpriced" },
        ]}
      />
    )
    const dl = container.querySelector("dl")!
    expect(dl.children).toHaveLength(2)
    expect(dl.style.gridTemplateColumns).toBe("repeat(2, minmax(0, 1fr))")
    expect(screen.getByText("Cost").tagName).toBe("DT")
    expect(screen.getByText("unknown")).toBeTruthy()
    expect(screen.getByText("1 model unpriced")).toBeTruthy()
  })
})

describe("StatusBadge", () => {
  it("always says the state in words", () => {
    render(
      <>
        {rows.map((r) => (
          <StatusBadge key={r.id} state={displayStatus(r)} />
        ))}
        <StatusBadge state="waiting" />
      </>
    )
    expect(screen.getByText("Interrupted")).toBeTruthy()
    expect(screen.getByText("Failed")).toBeTruthy()
    expect(screen.getAllByText("Succeeded")).toHaveLength(2)
    expect(screen.getByText("Waiting")).toBeTruthy()
  })
})

describe("JsonView", () => {
  it("shows the open levels and collapses the rest", () => {
    render(<JsonView value={rows[0]} depth={1} />)
    expect(screen.getByText('"r_stale"')).toBeTruthy()
    // model is one level down: collapsed, with its size.
    expect(screen.queryByText('"wefttest"')).toBeNull()
    fireEvent.click(screen.getByText(/"model":/).closest("button")!)
    expect(screen.getByText('"wefttest"')).toBeTruthy()
  })
})

describe("RelativeTime", () => {
  it("reads now in a cell and just now at the end of a sentence", () => {
    const time = new Date().toISOString()
    const { container, rerender } = render(<RelativeTime time={time} />)
    expect(container.textContent).toBe("now")
    rerender(<RelativeTime time={time} sentence />)
    expect(container.textContent).toBe("just now")
    rerender(
      <RelativeTime
        time={new Date(Date.now() - 90_000).toISOString()}
        sentence
      />
    )
    expect(container.textContent).toBe("1m ago")
  })
})

describe("ApprovalOutcome", () => {
  it("marks a run that skipped a call, and nothing else", () => {
    const { rerender, container } = render(
      <ApprovalOutcome outcome="expired" />
    )
    expect(screen.getByText("approval expired")).toBeTruthy()
    rerender(<ApprovalOutcome outcome="denied" />)
    expect(screen.getByText("approval denied")).toBeTruthy()
    rerender(<ApprovalOutcome outcome="approved" />)
    expect(container.textContent).toBe("")
    rerender(<ApprovalOutcome outcome="pending" />)
    expect(container.textContent).toBe("")
    rerender(<ApprovalOutcome outcome={undefined} />)
    expect(container.textContent).toBe("")
  })
})

describe("approvals_outcome on a runs row", () => {
  // Captured from the demo: one logical run per worst outcome.
  const outcomes = (runsOutcomes as unknown as RunsPage).runs
  const input = RUN_COLUMNS.find((c) => c.key === "input")!
  const shown = (o: string) => {
    const row = outcomes.find((r) => r.approvals_outcome === o)!
    const { container, unmount } = render(
      <TooltipProvider>{input.cell(row)}</TooltipProvider>
    )
    const text = container.textContent
    unmount()
    return text
  }
  it("says an expiry and a denial, and is quiet otherwise", () => {
    expect(shown("expired")).toMatch(/approval expired/)
    expect(shown("denied")).toMatch(/approval denied/)
    expect(shown("approved")).not.toMatch(/approval/)
    expect(shown("pending")).not.toMatch(/approval/)
  })
  it("reads the chain's outcome when the row has none of its own", () => {
    const row = outcomes.find((r) => r.approvals_outcome === "expired")!
    const { container } = render(
      <TooltipProvider>
        {input.cell({ ...row, approvals_outcome: undefined })}
      </TooltipProvider>
    )
    expect(container.textContent).toMatch(/approval expired/)
  })
})
