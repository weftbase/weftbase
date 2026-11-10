// The pure logic behind the Overview, the Runs list, Agents and
// Approvals, on bodies captured unedited from the demo server
// (`go run ./examples/demo`): runs-chains (GET /v1/runs?chains=1),
// stats-total, stats-agent-outcomes, stats-tool, agents-summary,
// agent-detail, tools, approvals.
import { describe, expect, it } from "vitest"

import {
  agentCounts,
  driftNote,
  inventoryHeader,
  labelVersions,
} from "@/components/console/agent/inventory"
import { niceMax, outcomeCounts } from "@/components/console/runs-chart"
import { filterChips, parseFilter } from "@/components/console/runs-filter"
import {
  changeLines,
  inventoryLine,
  labelPointers,
  limitRows,
  moveError,
  versionNumbers,
} from "@/lib/agents"
import { ApiError } from "@/lib/api/errors"
import type {
  AgentDetail,
  AgentsSummary,
  ApprovalsPage,
  RunRow,
  Stats,
  StatsTotal,
  ToolsDoc,
} from "@/lib/api/types"
import {
  approvalState,
  approvalVersion,
  argsSummary,
  decidedBy,
} from "@/lib/approval-view"
import { exactFirst, idCandidate, matches } from "@/lib/jump"
import {
  costUnknown,
  isSearching,
  rowCause,
  rowChildren,
  rowCost,
  rowIssues,
  rowStatus,
  rowSteps,
  rowTimes,
  rowTokens,
  rowVersion,
  toRunsQuery,
  unpricedLine,
} from "@/lib/runs"
import { validateApprovals, validateOverview, validateRuns } from "@/lib/search"
import { breakdownRows, figuresOf } from "@/lib/stats"

import agentDetail from "./fixtures/agent-detail.json"
import agentsSummary from "./fixtures/agents-summary.json"
import approvals from "./fixtures/approvals.json"
import runsChains from "./fixtures/runs-chains.json"
import statsAgent from "./fixtures/stats-agent-outcomes.json"
import statsTool from "./fixtures/stats-tool.json"
import statsTotal from "./fixtures/stats-total.json"
import tools from "./fixtures/tools.json"

const rows = runsChains.runs as unknown as RunRow[]
const pick = (f: (r: RunRow) => boolean) => {
  const r = rows.find(f)
  if (!r) throw new Error("the fixture has no such run")
  return r
}

describe("a row of the runs list", () => {
  const chained = pick((r) => !!r.chain && r.chain.runs.length > 1)
  const failed = pick((r) => r.error_type === "max_steps")
  const cancelled = pick((r) => r.display_status === "cancelled")
  const modelError = pick((r) => r.error_type === "model_error")
  const parent = pick((r) => (r.children_count ?? 0) > 0)

  it("shows a park and its resume as one run with the chain's totals", () => {
    // The head row parked ("succeeded", stopped_by parked); the chain
    // finished.
    expect(chained.stopped_by).toBe("parked")
    expect(rowStatus(chained)).toBe("succeeded")
    expect(rowSteps(chained)).toBe(chained.chain!.steps)
    expect(rowSteps(chained)).toBeGreaterThan(chained.steps)
    expect(rowTokens(chained)).toBe(
      chained.chain!.usage.input_tokens + chained.chain!.usage.output_tokens
    )
    const t = rowTimes(chained)
    expect(t.finished).toBe(chained.chain!.finished)
    expect(t.waitingMs).toBe(chained.chain!.waiting_ms)
    expect(t.durationMs).toBeGreaterThan(t.waitingMs)
  })

  it("never calls a cancelled run failed, and says who cancelled", () => {
    expect(rowStatus(cancelled)).toBe("cancelled")
    expect(rowCause(cancelled)).toBe("by user")
  })

  it("puts the cause of a failure in the tooltip", () => {
    expect(rowStatus(failed)).toBe("failed")
    expect(rowCause(failed)).toMatch(/^max_steps/)
  })

  it("lists the issue counters for the one glyph", () => {
    expect(rowIssues(failed)).toEqual(["4 tool errors"])
    expect(rowIssues(cancelled)).toEqual([])
  })

  it("has a child chip for a run that delegated", () => {
    expect(rowChildren(parent)).toEqual({ label: "1 subagent", failed: false })
    expect(rowChildren(failed)).toBeUndefined()
  })

  it("reads the version and the label the run resolved through", () => {
    expect(rowVersion(parent)).toEqual({ number: 1, label: "prod" })
  })

  it("shows a dash, not $0, for a run that used nothing", () => {
    // A model error before any usage: cost_usd is 0 and "known".
    expect(modelError.usage.input_tokens).toBe(0)
    expect(rowTokens(modelError)).toBeUndefined()
    expect(rowCost(modelError)).toBe("—")
    expect(costUnknown(modelError)).toBe(false)
  })

  it("marks a bill with an unpriced subagent as a lower bound", () => {
    expect(parent.cost_known).toBe(true)
    expect(parent.delegated_cost_known).toBe(false)
    expect(rowCost(parent)).toMatch(/^≥ \$/)
    expect(costUnknown(parent)).toBe(true)
  })

  it("says once, under the list, which costs are unknown and why", () => {
    const n = rows.filter(costUnknown).length
    expect(n).toBeGreaterThan(0)
    expect(unpricedLine(rows)).toBe(
      `${n} runs have no full cost: no price for acme/acme-billing-ft-1.`
    )
    expect(unpricedLine(rows.filter((r) => !costUnknown(r)))).toBeUndefined()
  })
})

describe("the runs list's filters", () => {
  it("keeps every filter in the URL and drops what it does not know", () => {
    const s = validateRuns({
      tool: "issue_refund",
      min_cost: "0.004",
      min_tokens: "1000",
      tag: "customer=acme",
      sort: "cost",
      sub: "true",
      error: "max_steps",
      bogus: "x",
    })
    expect(s).toMatchObject({
      tool: "issue_refund",
      min_cost: 0.004,
      min_tokens: 1000,
      tag: "customer=acme",
      sort: "cost",
      sub: true,
      error: "max_steps",
    })
    expect(validateRuns({ tag: "novalue", min_cost: "-1", sort: "x" })).toEqual(
      validateRuns({})
    )
  })

  it("asks the server for chains' statuses on a plain list", () => {
    const s = validateRuns({ status: "failed", agent: "support" })
    expect(isSearching(s)).toBe(false)
    expect(toRunsQuery(s)).toMatchObject({
      display_status: "failed",
      agent: "support",
    })
  })

  it("asks for the status as shown in a search too", () => {
    const s = validateRuns({ status: "failed", tool: "update_address" })
    expect(isSearching(s)).toBe(true)
    expect(toRunsQuery(s)).toMatchObject({
      display_status: "failed",
      tool: "update_address",
    })
  })

  it("maps the error kind, subagent runs and the tag", () => {
    expect(
      toRunsQuery(validateRuns({ error: "max_steps", sub: true, tag: "a=b" }))
    ).toMatchObject({ error_type: "max_steps", parent: "*", tag: "a=b" })
  })

  it("parses what is typed into a filter field", () => {
    expect(parseFilter("min_cost", "$0.01")).toEqual({ min_cost: 0.01 })
    expect(parseFilter("min_cost", "abc")).toBeUndefined()
    expect(parseFilter("min_tokens", "1,000")).toEqual({ min_tokens: 1000 })
    expect(parseFilter("tag", "customer=acme")).toEqual({
      tag: "customer=acme",
    })
    expect(parseFilter("tag", "customer")).toBeUndefined()
    expect(parseFilter("session", " ses_1 ")).toEqual({ session: "ses_1" })
  })

  it("names each applied filter on a chip", () => {
    expect(
      filterChips(
        validateRuns({ tool: "issue_refund", min_cost: 0.5, sub: true })
      ).map((c) => c.label)
    ).toEqual(["tool: issue_refund", "cost ≥ $0.50", "with subagent runs"])
  })
})

describe("the overview", () => {
  const total = statsTotal as unknown as StatsTotal

  it("reads the tiles from by=total, cancelled runs not counted as failures", () => {
    const t = total.total
    const f = figuresOf(t)
    expect(f.runs.value).toBe(String(t.runs))
    // failed / (succeeded + failed), not failed / runs.
    expect(t.failure_rate).toBeCloseTo(t.failed / (t.succeeded + t.failed), 5)
    expect(f.failureRate.hint).toBe(
      `${t.failed} of ${t.succeeded + t.failed} failed`
    )
    expect(f.failureRate.tone).toBe("danger")
    // Part of the cost is unpriced: a lower bound, never a bare figure.
    expect(t.cost_known).toBe(false)
    expect(f.cost.value).toMatch(/^≥ \$/)
  })

  it("reads no tokens and no cost as a dash", () => {
    const empty = total.series.find((b) => b.runs === 0)!
    const f = figuresOf(empty)
    expect(f.cost.value).toBe("—")
    expect(f.tokens.value).toBe("—")
    expect(f.failureRate.value).toBe("—")
  })

  it("splits each bucket by outcome, the rest being still open", () => {
    expect(total.series).toHaveLength(total.buckets)
    for (const b of total.series) {
      const c = outcomeCounts(b)
      expect(c.succeeded + c.cancelled + c.waiting + c.failed + c.open).toBe(
        b.runs
      )
    }
    const sum = total.series.reduce((a, b) => a + b.runs, 0)
    expect(sum).toBe(total.total.runs)
    expect(niceMax(68)).toBe(100)
    expect(niceMax(3)).toBe(4)
  })

  it("breaks down by agent on outcomes, with links to the runs", () => {
    const stats = statsAgent as unknown as Stats
    const rs = breakdownRows("agent", stats)
    expect(rs.map((r) => r.key)).toEqual(["support", "refunds"])
    const support = rs[0]
    const o = stats.groups.find((g) => g.key === "support")!.outcomes!
    // The trace store's "failed" counts cancelled runs; outcomes do not.
    expect(support.runs).toBe(o.runs)
    expect(support.failureRate).toBe(o.failure_rate)
    expect(support.failureNote).toBe(
      `${o.failed} of ${o.succeeded + o.failed} finished runs failed`
    )
    expect(support.spark).toHaveLength(stats.buckets)
    expect(support.spark.reduce((a, p) => a + p.total, 0)).toBe(o.runs)
    expect(support.link).toEqual({ agent: "support" })
    expect(support.failedLink).toEqual({ agent: "support", status: "failed" })
  })

  it("breaks down by tool per call, with no tokens or cost to show", () => {
    const stats = statsTool as unknown as Stats
    const rs = breakdownRows("tool", stats)
    const addr = rs.find((r) => r.key === "update_address")!
    expect(addr.failureRate).toBe(1)
    expect(addr.failureNote).toMatch(/^\d+ of \d+ calls failed$/)
    expect(addr.tokens).toBeUndefined()
    expect(addr.cost).toBe("—")
    expect(addr.link).toEqual({ tool: "update_address", sub: true })
    // Most runs first.
    expect(rs.map((r) => r.runs)).toEqual(
      [...rs.map((r) => r.runs)].sort((a, b) => b - a)
    )
  })

  it("leaves cancelled out of the error kinds", () => {
    const stats = statsTool as unknown as Stats
    const fake = {
      ...stats,
      groups: stats.groups.slice(0, 2).map((g, i) => ({
        ...g,
        key: i === 0 ? "cancelled" : "max_steps",
      })),
    }
    const rs = breakdownRows("error", fake)
    expect(rs.map((r) => r.key)).toEqual(["max_steps"])
    expect(rs[0].failureRate).toBeNull()
    expect(rs[0].link).toEqual({ error: "max_steps", status: "failed" })
  })

  it("keeps the window and the segment in the URL", () => {
    expect(validateOverview({ range: "7d", by: "tool" })).toEqual({
      range: "7d",
      by: "tool",
    })
    // 30d is a Runs range, not an Overview window.
    expect(validateOverview({ range: "30d", by: "x" })).toEqual({
      range: undefined,
      by: undefined,
    })
  })
})

describe("the agent inventory", () => {
  const summary = agentsSummary as unknown as AgentsSummary
  const detail = agentDetail as unknown as AgentDetail
  const toolsDoc = tools as unknown as ToolsDoc

  it("counts it in the header", () => {
    expect(inventoryLine(summary.totals)).toBe(
      "2 agents · 12 tools · 2 placements"
    )
    expect(inventoryLine({ agents: 1, tools: 1, placements: null })).toBe(
      "1 agent · 1 tool · 0 placements"
    )
  })

  it("counts registered agents, and says how many are not", () => {
    // The rows hold one agent no build registered; `totals.agents`
    // counts the registered ones only. The header counts the rows.
    expect(summary.agents.length).toBe(summary.totals.agents + 1)
    expect(agentCounts(summary.agents)).toEqual({
      registered: summary.totals.agents,
      unregistered: 1,
    })
    expect(inventoryHeader(summary)).toBe(
      "2 agents · 1 not registered · 12 tools · 2 placements"
    )
    expect(
      inventoryHeader({
        agents: summary.agents.filter((a) => a.registered).slice(0, 1),
        totals: { agents: 1, tools: 1, placements: null },
      })
    ).toBe("1 agent · 1 tool · 0 placements")
  })

  it("reads where a label points from the server", () => {
    expect(
      labelVersions({
        labels: { staging: "agv_b", prod: "agv_a", canary: "agv_c" },
        label_versions: {
          prod: { id: "agv_a", number: 7 },
          staging: { id: "agv_b", number: 8 },
        },
      })
    ).toEqual([
      { label: "prod", version: "agv_a", number: 7 },
      { label: "canary", version: "agv_c", number: undefined },
      { label: "staging", version: "agv_b", number: 8 },
    ])
  })

  it("says how many runs ran a different manifest", () => {
    expect(driftNote(undefined)).toBeUndefined()
    expect(
      driftNote({ drift: false, runs_checked: 4, runs_drifted: 0 })
    ).toBeUndefined()
    expect(driftNote({ drift: true, runs_checked: 6, runs_drifted: 5 })).toBe(
      "5 of 6 runs ran a different manifest than the one this version registered."
    )
    expect(
      driftNote({
        drift: true,
        runs_checked: 1,
        runs_drifted: 1,
        since: "2026-11-10T12:49:24Z",
      })
    ).toMatch(/^1 of 1 run since .+ ran a different manifest/)
  })

  it("names an approval's version as the run page does", () => {
    expect(
      approvalVersion({ id: "agv_1", number: 2, labels: ["staging"] })
    ).toEqual({ id: "agv_1", number: 2, labels: ["staging"] })
    expect(approvalVersion({ id: "agv_1", number: 2, labels: null })).toEqual({
      id: "agv_1",
      number: 2,
      labels: [],
    })
    expect(approvalVersion(undefined)).toBeUndefined()
  })

  it("knows which agents are not registered", () => {
    expect(
      summary.agents.filter((a) => !a.registered).map((a) => a.name)
    ).toEqual(["billing"])
  })

  it("shows the version each label points to", () => {
    const numbers = versionNumbers(summary.agents, toolsDoc)
    const support = summary.agents.find((a) => a.name === "support")!
    expect(
      labelPointers(support.labels, numbers).map((p) => [p.label, p.number])
    ).toEqual([
      ["prod", 1],
      ["staging", 2],
    ])
    // Without the tool inventory only the newest version's number is known.
    expect(
      labelPointers(support.labels, versionNumbers(summary.agents)).map(
        (p) => p.number
      )
    ).toEqual([undefined, 2])
  })

  it("says what changed in a version", () => {
    const [v2, v1] = detail.version_list
    expect(v2.number).toBe(2)
    expect(changeLines(v2.changes)).toEqual(["added handoff_to_human"])
    expect(changeLines(v1.changes)).toEqual(["first version"])
    expect(
      changeLines({
        ...v2.changes!,
        tools_added: [],
        same_manifest: true,
      })
    ).toEqual(["new build, same manifest"])
    expect(
      changeLines({
        ...v2.changes!,
        tools_added: [],
        instructions_changed: true,
        model_changed: {
          from: { provider: "openai", name: "gpt-4o-mini" },
          to: { provider: "openai", name: "gpt-4o" },
        },
        limits_changed: [{ field: "max_steps", from: 4, to: 8 }],
        tools_changed: [{ name: "billing", fields: ["needs_approval"] }],
      })
    ).toEqual([
      "model gpt-4o-mini → gpt-4o",
      "instructions changed",
      "billing: needs approval changed",
      "max steps 4 → 8",
    ])
  })

  it("lists every limit the manifest sets and no other", () => {
    const limits = limitRows(detail.newest!.manifest!.limits)
    expect(limits).toEqual([
      ["Max steps", "4"],
      ["Model retries", "3"],
      ["Parallel tool calls", "4"],
      ["Result cap", "65.5 kB per tool result"],
    ])
    expect(
      limitRows({
        detect_loops: 3,
        stop_when: ["has_tool_call:submit"],
        max_input_tokens: 100000,
        timeout: "30s",
        strict_input: true,
      }).map((r) => r[0])
    ).toEqual([
      "Loop detection",
      "Stop conditions",
      "Usage limit",
      "Tool timeout",
      "Strict input",
    ])
  })

  it("explains a refused label move in a sentence", () => {
    const e = new ApiError(409, "conflict", "label moved")
    expect(moveError(e, "prod")).toMatch(
      /^The prod label was moved by someone else/
    )
    expect(moveError(new Error("boom"), "prod")).toBe("boom")
  })

  it("answers who can call a tool", () => {
    const refund = toolsDoc.tools.find((t) => t.name === "issue_refund")!
    expect(refund.needs_approval).toBe(true)
    expect(refund.agents.map((a) => a.agent)).toEqual(["refunds"])
    const billing = toolsDoc.tools.find((t) => t.name === "billing")!
    expect(billing.subagent).toBe("billing")
  })
})

describe("approvals", () => {
  const page = approvals as unknown as ApprovalsPage

  it("lists pending first", () => {
    const states = page.approvals.map((a) => a.status)
    const firstDecided = states.findIndex((s) => s !== "pending")
    expect(firstDecided).toBe(page.pending)
    expect(states.slice(firstDecided).includes("pending")).toBe(false)
  })

  it("puts the arguments on one line", () => {
    expect(argsSummary(page.approvals[0].args)).toMatch(
      /^order_id \d+ · amount_usd \d+(\.\d+)? · reason /
    )
    expect(argsSummary(null)).toBe("")
    expect(argsSummary({ a: [1, 2], b: { c: 1 } })).toBe("a [2] · b {…}")
  })

  it("names the state and who decided", () => {
    const pending = page.approvals[0]
    const denied = page.approvals.find((a) => a.status === "denied")!
    expect(approvalState(pending)).toEqual({
      label: "Pending",
      tone: "waiting",
    })
    expect(decidedBy(pending)).toBeUndefined()
    expect(approvalState(denied).label).toBe("Denied")
    expect(decidedBy(denied)).toMatch(/^API key key_/)
    expect(denied.args_sha256).toMatch(/^[0-9a-f]{64}$/)
  })

  it("keeps the inbox's selection and filters in the URL", () => {
    expect(
      validateApprovals({ id: "appr_1", status: "decided", tool: "x", junk: 1 })
    ).toEqual({
      id: "appr_1",
      status: "decided",
      agent: undefined,
      tool: "x",
      run: undefined,
    })
    expect(validateApprovals({ status: "nope" }).status).toBeUndefined()
  })
})

describe("the command menu", () => {
  it("looks a single long token up as an id", () => {
    expect(idCandidate(" run_01M72K20MR ")).toBe("run_01M72K20MR")
    expect(idCandidate("refund order")).toBeUndefined()
    expect(idCandidate("run")).toBeUndefined()
  })

  it("puts the run that was typed before its child runs", () => {
    const hits = [{ id: "run_1/1/call_a" }, { id: "run_1" }, { id: "run_12" }]
    expect(exactFirst(hits, "run_1").map((h) => h.id)).toEqual([
      "run_1",
      "run_12",
      "run_1/1/call_a",
    ])
  })

  it("filters pages and agents by every word", () => {
    expect(matches("", "Runs")).toBe(true)
    expect(matches("sup age", "support", "agent")).toBe(true)
    expect(matches("refund", "support", "agent")).toBe(false)
  })
})
