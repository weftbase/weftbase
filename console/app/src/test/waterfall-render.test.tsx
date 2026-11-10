// The waterfall mounted: compact mode (what a session turn embeds) and
// full mode, on a small tree. jsdom has no layout, so this checks what
// is there, not where.
import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest"

import { TraceWaterfall } from "@/components/console/trace-waterfall"
import type { Span } from "@/lib/api/types"

const at = (ms: number) =>
  new Date(Date.parse("2026-01-01T00:00:00Z") + ms).toISOString()
const span = (
  id: string,
  parent: string,
  op: string,
  name: string,
  a: number,
  b: number,
  extra: Record<string, unknown> = {}
): Span => ({
  trace_id: "t",
  span_id: id,
  parent_span_id: parent,
  name: `${op} ${name}`,
  kind: "internal",
  start: at(a),
  end: at(b),
  status: "ok",
  status_message: "",
  service: "svc",
  attrs: { "gen_ai.operation.name": op, ...extra },
  events: [],
  run_id: "r",
})
const spans = [
  span("a", "", "invoke_agent", "support", 0, 3000, {
    "gen_ai.agent.name": "support",
  }),
  span("b", "a", "chat", "gpt", 0, 1000, {
    "gen_ai.request.model": "gpt",
    "weft.step.index": 0,
  }),
  span("c", "a", "execute_tool", "lookup_order", 1000, 3000, {
    "gen_ai.tool.name": "lookup_order",
    "weft.step.index": 0,
  }),
]

beforeAll(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      disconnect() {}
      unobserve() {}
    }
  )
})
afterEach(cleanup)

describe("TraceWaterfall", () => {
  it("compact: rows and ruler, no search, transport or legend", () => {
    const onSelect = vi.fn()
    render(
      <TraceWaterfall
        compact
        trace={{ spans, rootRunId: "r" }}
        onSelect={onSelect}
      />
    )
    expect(screen.getAllByRole("treeitem")).toHaveLength(3)
    expect(screen.queryByLabelText("Search steps")).toBeNull()
    expect(screen.queryByLabelText("Play")).toBeNull()
    expect(screen.queryByText("Model call")).toBeNull()
    fireEvent.click(screen.getByText("lookup_order", { exact: true }))
    expect(onSelect).toHaveBeenCalledWith("c")
  })

  it("full: search filters, the transport is there, the word is Playback", () => {
    const { container } = render(
      <TraceWaterfall trace={{ spans, rootRunId: "r" }} selected="c" />
    )
    expect(screen.getByLabelText("Play")).toBeTruthy()
    expect(screen.getByLabelText("Playback speed")).toBeTruthy()
    expect(screen.getByText("Slowest")).toBeTruthy()
    expect(
      screen.getByRole("treeitem", { selected: true }).textContent
    ).toContain("lookup_order")
    fireEvent.change(screen.getByLabelText("Search steps"), {
      target: { value: "nothing like this" },
    })
    expect(screen.getByText("No matching steps")).toBeTruthy()
    fireEvent.change(screen.getByLabelText("Search steps"), {
      target: { value: "lookup" },
    })
    expect(screen.getAllByRole("treeitem")).toHaveLength(3)
    expect(container.textContent?.toLowerCase()).not.toContain("replay")
  })

  it("keyboard: arrows move, Enter opens, Esc closes", () => {
    const onSelect = vi.fn()
    render(
      <TraceWaterfall
        trace={{ spans, rootRunId: "r" }}
        selected="b"
        onSelect={onSelect}
      />
    )
    const tree = screen.getByRole("tree")
    fireEvent.keyDown(tree, { key: "ArrowDown" })
    fireEvent.keyDown(tree, { key: "Enter" })
    expect(onSelect).toHaveBeenLastCalledWith("c")
    fireEvent.keyDown(tree, { key: "Escape" })
    expect(onSelect).toHaveBeenLastCalledWith(undefined)
  })

  it("says so when there are no spans", () => {
    render(
      <TraceWaterfall
        trace={{ spans: [] }}
        empty="Waiting for the first step…"
      />
    )
    expect(screen.getByText("Waiting for the first step…")).toBeTruthy()
  })
})
