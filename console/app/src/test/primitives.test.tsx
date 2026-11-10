// The shared primitives render what they are given, plainly.
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it } from "vitest"

import { DataTable } from "@/components/console/data-table"
import { JsonView } from "@/components/console/json-view"
import { StatStrip } from "@/components/console/stat-strip"
import { StatusBadge } from "@/components/console/status"
import type { RunRow } from "@/lib/api/types"
import { displayStatus } from "@/lib/api/status"

import runs from "./fixtures/runs.json"

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
